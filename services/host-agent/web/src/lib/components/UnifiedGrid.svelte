<script lang="ts">
	import AppIcon from './AppIcon.svelte';
	import Widget from '$lib/widgets/Widget.svelte';
	import { getWidgetById, type WidgetDefinition } from '$lib/widgets/registry';
	import { layout, getWidgetConfig, type GridItem } from '$lib/stores/layout';
	import { visibleApps } from '$lib/stores/apps';
	import { type App } from '$lib/types';

	const GRID_COLS = 6;

	interface Props {
		onAppClick?: (app: App) => void;
		onAppContextMenu?: (e: MouseEvent, app: App) => void;
		onWidgetConfigure?: (widgetId: string) => void;
		onAddWidget?: () => void;
	}

	let { onAppClick, onAppContextMenu, onWidgetConfigure, onAddWidget }: Props = $props();

	// Local optimistic state for drag operations
	let localItems = $state<GridItem[]>([]);
	let isDragging = $state(false);
	let draggedItemId = $state<string | null>(null);
	let dragOverCell = $state<{ col: number; row: number } | null>(null);
	// Snapshot of layout when drag started - used as base for all reflow calculations
	let dragStartItems = $state<GridItem[]>([]);

	// Sync local state with store when not dragging
	$effect(() => {
		if (!isDragging) {
			localItems = [...$layout.items];
		}
	});

	// Calculate grid rows needed
	let gridRows = $derived.by(() => {
		if (localItems.length === 0) return 1;
		return Math.max(...localItems.map((item) => item.row + item.rowspan - 1), 1);
	});

	// Resolve grid items to their full data
	interface ResolvedItem {
		item: GridItem;
		app?: App;
		widget?: WidgetDefinition;
	}

	let resolvedItems = $derived.by(() => {
		const apps = $visibleApps;
		const appMap = new Map(apps.map((a) => [a.name, a]));

		return localItems
			.map((item) => {
				if (item.type === 'app') {
					const app = appMap.get(item.id);
					if (!app) return null;
					return { item, app } as ResolvedItem;
				} else {
					const widget = getWidgetById(item.id);
					if (!widget) return null;
					return { item, widget } as ResolvedItem;
				}
			})
			.filter((r): r is ResolvedItem => r !== null);
	});

	// Check if two items overlap
	function itemsOverlap(a: GridItem, b: GridItem): boolean {
		const aEndCol = a.col + a.colspan - 1;
		const aEndRow = a.row + a.rowspan - 1;
		const bEndCol = b.col + b.colspan - 1;
		const bEndRow = b.row + b.rowspan - 1;

		return (
			a.col <= bEndCol &&
			aEndCol >= b.col &&
			a.row <= bEndRow &&
			aEndRow >= b.row
		);
	}

	// Check if item fits within grid bounds
	function fitsInBounds(col: number, colspan: number): boolean {
		return col >= 1 && col + colspan - 1 <= GRID_COLS;
	}

	// Find all items that would overlap with the dragged item at the target position
	function findOverlappingItems(items: GridItem[], draggedId: string, targetCol: number, targetRow: number, colspan: number, rowspan: number): GridItem[] {
		const targetItem: GridItem = {
			type: 'app',
			id: '__target__',
			col: targetCol,
			row: targetRow,
			colspan,
			rowspan,
		};

		return items.filter((item) => item.id !== draggedId && itemsOverlap(item, targetItem));
	}

	// Find the next available position for an item, searching row by row
	function findAvailablePosition(items: GridItem[], excludeId: string, colspan: number, rowspan: number, startRow: number = 1): { col: number; row: number } {
		const maxRow = Math.max(...items.map((i) => i.row + i.rowspan - 1), startRow) + 10;

		for (let row = startRow; row <= maxRow; row++) {
			for (let col = 1; col <= GRID_COLS - colspan + 1; col++) {
				const testItem: GridItem = {
					type: 'app',
					id: '__test__',
					col,
					row,
					colspan,
					rowspan,
				};

				const hasOverlap = items.some((item) => item.id !== excludeId && itemsOverlap(item, testItem));
				if (!hasOverlap) {
					return { col, row };
				}
			}
		}

		return { col: 1, row: maxRow + 1 };
	}

	// Reflow items to resolve any overlaps after a move
	function reflowItems(items: GridItem[], movedItemId: string): GridItem[] {
		const result = [...items];
		const movedItem = result.find((i) => i.id === movedItemId);
		if (!movedItem) return result;

		// Find items that overlap with the moved item
		const overlapping = findOverlappingItems(result, movedItemId, movedItem.col, movedItem.row, movedItem.colspan, movedItem.rowspan);

		// Move each overlapping item to the next available position
		for (const item of overlapping) {
			const itemIndex = result.findIndex((i) => i.id === item.id);
			if (itemIndex === -1) continue;

			// Find a new position for this item
			const newPos = findAvailablePosition(result, item.id, item.colspan, item.rowspan, 1);
			result[itemIndex] = { ...item, col: newPos.col, row: newPos.row };
		}

		return result;
	}

	// Get cell position from mouse event
	function getCellFromEvent(e: DragEvent, gridEl: HTMLElement): { col: number; row: number } | null {
		const rect = gridEl.getBoundingClientRect();
		const x = e.clientX - rect.left;
		const y = e.clientY - rect.top;

		const cellWidth = rect.width / GRID_COLS;
		const cellHeight = 100; // Fixed row height

		const col = Math.floor(x / cellWidth) + 1;
		const row = Math.floor(y / cellHeight) + 1;

		if (col < 1 || col > GRID_COLS || row < 1) return null;
		return { col, row };
	}

	// Drag handlers
	function handleDragStart(e: DragEvent, item: GridItem) {
		isDragging = true;
		draggedItemId = item.id;
		// Store snapshot of current layout as base for reflow calculations
		dragStartItems = [...$layout.items];
		if (e.dataTransfer) {
			e.dataTransfer.effectAllowed = 'move';
			e.dataTransfer.setData('text/plain', item.id);
		}
	}

	function handleDragOver(e: DragEvent) {
		e.preventDefault();
		if (!draggedItemId) return;

		const gridEl = e.currentTarget as HTMLElement;
		const cell = getCellFromEvent(e, gridEl);
		if (!cell) return;

		// Use the original dragged item from drag start snapshot
		const draggedItem = dragStartItems.find((i) => i.id === draggedItemId);
		if (!draggedItem) return;

		// Check if position actually changed
		if (dragOverCell && dragOverCell.col === cell.col && dragOverCell.row === cell.row) {
			return;
		}

		// Check if the item would fit in bounds at this position
		if (!fitsInBounds(cell.col, draggedItem.colspan)) {
			return;
		}

		dragOverCell = cell;

		// Start from the original layout snapshot, then apply the new position
		let newItems = dragStartItems.map((item) =>
			item.id === draggedItemId ? { ...item, col: cell.col, row: cell.row } : item
		);

		// Reflow to resolve any overlaps
		newItems = reflowItems(newItems, draggedItemId);

		localItems = newItems;

		if (e.dataTransfer) {
			e.dataTransfer.dropEffect = 'move';
		}
	}

	function handleDrop(e: DragEvent) {
		e.preventDefault();
		if (!draggedItemId) {
			resetDragState();
			return;
		}

		// Commit to store (local state is already updated)
		// Even if dragOverCell is null (dropped in same spot), persist current localItems
		layout.setItems(localItems);
		resetDragState();
	}

	function handleDragEnd() {
		// handleDragEnd fires after handleDrop, but handleDrop already calls resetDragState
		// So isDragging will be false if drop was successful
		// Only revert if drag was cancelled (e.g., pressed Escape or dropped outside)
		if (isDragging) {
			localItems = [...$layout.items];
			resetDragState();
		}
	}

	function resetDragState() {
		isDragging = false;
		draggedItemId = null;
		dragOverCell = null;
		dragStartItems = [];
	}

	function handleWidgetRemove(widgetId: string) {
		layout.removeWidget(widgetId);
	}
</script>

<div class="unified-grid-container">
	{#if resolvedItems.length === 0}
		<div class="empty-state">
			<p>No apps or widgets yet</p>
			{#if onAddWidget}
				<button class="add-widget-btn" onclick={onAddWidget}>Add Widget</button>
			{/if}
		</div>
	{:else}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="unified-grid"
			class:is-dragging={isDragging}
			style="--grid-rows: {gridRows + 1};"
			role="list"
			ondragover={handleDragOver}
			ondrop={handleDrop}
		>
			{#each resolvedItems as resolved (resolved.item.id)}
				{@const isBeingDragged = draggedItemId === resolved.item.id}
				{@const isApp = resolved.item.type === 'app'}

				<div
					class="grid-item"
					class:dragging={isBeingDragged}
					class:is-app={isApp}
					class:is-widget={!isApp}
					style="
						grid-column: {resolved.item.col} / span {resolved.item.colspan};
						grid-row: {resolved.item.row} / span {resolved.item.rowspan};
					"
					draggable="true"
					ondragstart={(e) => handleDragStart(e, resolved.item)}
					ondragend={handleDragEnd}
					role="listitem"
				>
					{#if isApp && resolved.app}
						<button
							class="app-slot"
							onclick={() => onAppClick?.(resolved.app!)}
							oncontextmenu={(e) => onAppContextMenu?.(e, resolved.app!)}
						>
							<div class="app-icon-container">
								<AppIcon appName={resolved.app.name} displayName={resolved.app.display_name} size="lg" />
							</div>
							<span class="app-label">{resolved.app.display_name}</span>
						</button>
					{:else if resolved.widget}
						<Widget title={resolved.widget.name} onRemove={() => handleWidgetRemove(resolved.widget!.id)}>
							<resolved.widget.component
								config={getWidgetConfig(resolved.widget.id)}
								onConfigure={resolved.widget.configurable ? () => onWidgetConfigure?.(resolved.widget!.id) : undefined}
							/>
						</Widget>
					{/if}
				</div>
			{/each}

			<!-- Add widget button in bottom-right corner -->
			{#if onAddWidget}
				<button
					class="add-widget-cell"
					style="grid-column: {GRID_COLS}; grid-row: {gridRows + 1};"
					onclick={onAddWidget}
					aria-label="Add widget"
				>
					<span class="add-icon">+</span>
				</button>
			{/if}
		</div>
	{/if}
</div>

<style>
	.unified-grid-container {
		width: 100%;
		max-width: 1000px;
		margin: 0 auto;
		container-type: inline-size;
	}

	.unified-grid {
		display: grid;
		grid-template-columns: repeat(6, 1fr);
		grid-template-rows: repeat(var(--grid-rows, 1), 100px);
		gap: var(--space-md);
	}

	.grid-item {
		transition:
			transform 0.2s cubic-bezier(0.2, 0, 0, 1),
			opacity 0.2s ease;
	}

	/* Smooth position transitions when items move in the grid */
	.unified-grid.is-dragging .grid-item:not(.dragging) {
		transition:
			transform 0.25s cubic-bezier(0.2, 0, 0, 1),
			opacity 0.2s ease;
	}

	.grid-item.is-app {
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.grid-item.is-widget {
		display: flex;
		flex-direction: column;
	}

	.grid-item.is-widget :global(.widget) {
		flex: 1;
		display: flex;
		flex-direction: column;
	}

	.grid-item.is-widget :global(.widget-content) {
		flex: 1;
	}

	.grid-item.dragging {
		opacity: 0.5;
		transform: scale(0.95);
		z-index: 100;
	}

	.grid-item:not(.dragging) {
		cursor: grab;
	}

	.grid-item:active:not(.dragging) {
		cursor: grabbing;
	}

	/* App slot styling */
	.app-slot {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-xs);
		padding: var(--space-sm);
		background: transparent;
		border: none;
		cursor: pointer;
		transition: transform 0.1s ease;
		width: 100%;
		height: 100%;
	}

	.app-slot:hover {
		transform: scale(1.05);
	}

	.app-slot:active {
		transform: scale(0.95);
	}

	.app-icon-container {
		width: 52px;
		height: 52px;
	}

	.app-icon-container :global(.app-icon.size-lg) {
		width: 52px;
		height: 52px;
	}

	.app-icon-container :global(.app-icon.size-lg img) {
		width: 38px;
		height: 38px;
	}

	.app-label {
		font-family: var(--font-sans);
		font-size: 11px;
		font-weight: 500;
		line-height: 1.2;
		color: var(--color-text);
		text-align: center;
		max-width: 80px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	/* Add widget button */
	.add-widget-cell {
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--color-bg-elevated);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-lg);
		cursor: pointer;
		transition: border-color 0.15s ease, background 0.15s ease;
	}

	.add-widget-cell:hover {
		border-color: var(--color-text-muted);
		background: var(--color-bg-subtle);
	}

	.add-icon {
		width: 24px;
		height: 24px;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 1.25rem;
		color: var(--color-text-muted);
	}

	/* Empty state */
	.empty-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-md);
		padding: var(--space-3xl);
		color: var(--color-text-muted);
	}

	.empty-state p {
		margin: 0;
	}

	.add-widget-btn {
		padding: var(--space-sm) var(--space-lg);
		background: var(--color-accent);
		color: white;
		border: none;
		border-radius: var(--radius-md);
		font-family: var(--font-serif);
		cursor: pointer;
	}

	.add-widget-btn:hover {
		background: var(--color-accent-hover);
	}

	/* Responsive: 4 columns */
	@container (max-width: 700px) {
		.unified-grid {
			grid-template-columns: repeat(4, 1fr);
		}
	}

	/* Responsive: 3 columns */
	@container (max-width: 500px) {
		.unified-grid {
			grid-template-columns: repeat(3, 1fr);
			grid-template-rows: repeat(var(--grid-rows, 1), 90px);
		}
	}

	/* Responsive: 2 columns */
	@container (max-width: 350px) {
		.unified-grid {
			grid-template-columns: repeat(2, 1fr);
		}
	}
</style>
