<script lang="ts">
	import { onMount, onDestroy, mount, unmount } from 'svelte';
	import { GridStack, type GridStackNode } from 'gridstack';
	import { gridElements, type GridElement } from '$lib/stores/grid';
	import { getWidgetById } from '$lib/widgets/registry';
	import { saveLayout } from '$lib/clients/layoutClient';
	import { type App } from '$lib/types';
	import AppTile from './AppTile.svelte';
	import WidgetWrapper from './WidgetWrapper.svelte';

	interface Props {
		onAppClick?: (app: App) => void;
		onAppContextMenu?: (e: MouseEvent, app: App) => void;
		onAddWidget?: () => void;
	}

	let { onAppClick, onAppContextMenu, onAddWidget }: Props = $props();

	let gridEl: HTMLElement;
	let grid: GridStack;

	// Track mounted Svelte component instances by item id for cleanup
	const mountedComponents = new Map<string, ReturnType<typeof mount>>();

	// Prevent sending PUT requests for programmatic position syncs.
	// True while syncGridFromStore is running; false during user drag/resize.
	let suppressLayoutSave = false;

	// Prevent syncGridFromStore from interrupting an active user drag.
	let isDragging = false;

	/**
	 * Add a single GridStack item and mount its Svelte component into it.
	 * Passes autoPosition: true when x/y are null so GridStack picks the cell.
	 */
	function addGridStackItem(element: GridElement) {
		const isApp = element.type === 'app';
		const widget = isApp ? null : getWidgetById(element.id);
		const hasPosition = element.x !== null && element.y !== null;

		const node = grid.addWidget({
			id: element.id,
			...(hasPosition ? { x: element.x!, y: element.y! } : { autoPosition: true }),
			w: element.w,
			h: element.h,
			noResize: isApp,
			noMove: false,
			minW: isApp ? 1 : (widget?.size.cols ?? 1),
			minH: isApp ? 1 : (widget?.size.rows ?? 1),
		});

		const contentEl = node?.querySelector('.grid-stack-item-content');
		if (!contentEl) return;

		let instance: ReturnType<typeof mount>;
		if (isApp) {
			instance = mount(AppTile, {
				target: contentEl as HTMLElement,
				props: { itemId: element.id, onAppClick, onAppContextMenu },
			});
		} else if (widget) {
			instance = mount(WidgetWrapper, {
				target: contentEl as HTMLElement,
				props: {
					widget,
					onRemove: () => {
						gridElements.removeWidget(element.id);
					},
				},
			});
		} else {
			return;
		}

		mountedComponents.set(element.id, instance);
	}

	/**
	 * Sync the GridStack DOM to match the provided element list.
	 * - Removes items not in the list.
	 * - Adds new items (with autoPosition if x/y null).
	 * - Updates positions for existing items only when not dragging.
	 *
	 * Sets suppressLayoutSave=true so the resulting 'change' events don't
	 * trigger a PUT — except when new items are added (their auto-placed
	 * positions need to be persisted).
	 */
	function syncGridFromStore(elements: GridElement[]) {
		if (!grid) return;

		const storeIds = new Set(elements.map((e) => e.id));
		let hasStructuralChange = false;

		suppressLayoutSave = true;
		grid.batchUpdate(true);

		// Remove items that are no longer in the store
		for (const node of grid.getGridItems()) {
			const id = node.gridstackNode?.id as string | undefined;
			if (id && !storeIds.has(id)) {
				const instance = mountedComponents.get(id);
				if (instance) {
					unmount(instance);
					mountedComponents.delete(id);
				}
				grid.removeWidget(node, true, false);
				hasStructuralChange = true;
			}
		}

		// Build the current id set after removals
		const currentIds = new Set(
			grid
				.getGridItems()
				.map((n) => n.gridstackNode?.id as string | undefined)
				.filter(Boolean)
		);

		for (const element of elements) {
			// New item — add it (autoPosition when x/y null)
			if (!currentIds.has(element.id)) {
				addGridStackItem(element);
				hasStructuralChange = true;
				continue;
			}

			// Update position/size for existing items when not dragging
			if (isDragging || element.x === null || element.y === null) continue;

			const node = grid
				.getGridItems()
				.find((n) => (n.gridstackNode?.id as string) === element.id);
			const gsn = node?.gridstackNode;
			if (!node || !gsn) continue;

			if (
				gsn.x !== element.x ||
				gsn.y !== element.y ||
				gsn.w !== element.w ||
				gsn.h !== element.h
			) {
				grid.update(node, { x: element.x, y: element.y, w: element.w, h: element.h });
			}
		}

		// Allow the 'change' event from auto-positioned new items to fire a PUT.
		// Position-only updates from the poller should not trigger a PUT.
		if (hasStructuralChange) {
			suppressLayoutSave = false;
		}

		grid.batchUpdate(false);
		suppressLayoutSave = false;
	}

	/**
	 * Called by GridStack on any drag/resize change event.
	 * Sends the full settled layout to PUT /api/user/layout.
	 */
	function handleGridChange(_event: Event, nodes: GridStackNode[]) {
		if (suppressLayoutSave) return;
		if (!nodes || nodes.length === 0) return;

		const currentStore = $gridElements;
		const storeMap = new Map(currentStore.map((e) => [e.id, e]));

		const settled = grid.getGridItems().map((item) => {
			const gsn = item.gridstackNode!;
			const id = gsn.id as string;
			const existing = storeMap.get(id);
			return {
				type: existing?.type ?? 'app',
				id,
				x: gsn.x ?? 0,
				y: gsn.y ?? 0,
				w: gsn.w ?? 1,
				h: gsn.h ?? 1,
			} satisfies GridElement;
		});

		saveLayout(settled);
	}

	onMount(() => {
		grid = GridStack.init(
			{
				column: 6,
				cellHeight: 100,
				margin: 8,
				float: false,
				animate: true,
				handleClass: 'widget-header',
				columnOpts: {
					breakpointForWindow: false,
					breakpoints: [
						{ w: 350, c: 2 },
						{ w: 500, c: 3 },
						{ w: 700, c: 4 },
					],
				},
			},
			gridEl
		);

		// Track drag/resize state so syncGridFromStore doesn't interrupt user
		grid.on('dragstart resizestart', () => { isDragging = true; });
		grid.on('dragstop resizestop', () => { isDragging = false; });

		// Initial population from store
		syncGridFromStore($gridElements);

		// Listen for drag/resize changes
		grid.on('change', handleGridChange);
	});

	// React to store changes (poller update, widget toggle, app install/uninstall)
	$effect(() => {
		const elements = $gridElements;
		syncGridFromStore(elements);
	});

	onDestroy(() => {
		for (const [, instance] of mountedComponents) {
			unmount(instance);
		}
		mountedComponents.clear();

		if (grid) {
			grid.off('change');
			grid.destroy(false);
		}
	});
</script>

<div class="gridstack-container">
	<div class="grid-stack" bind:this={gridEl}></div>

	{#if onAddWidget}
		<div class="add-widget-bar">
			<button class="add-widget-btn" onclick={onAddWidget} aria-label="Add widget">
				<span class="add-icon">+</span>
				<span>Add Widget</span>
			</button>
		</div>
	{/if}
</div>

<style>
	.gridstack-container {
		width: 100%;
		max-width: 1000px;
		margin: 0 auto;
	}

	/* Make GridStack item backgrounds transparent — our components supply their own backgrounds */
	.gridstack-container :global(.grid-stack-item-content) {
		background: transparent;
		border-radius: 0;
		overflow: visible;
	}

	/* Drag placeholder styling */
	.gridstack-container :global(.grid-stack-placeholder > .placeholder-content) {
		background: var(--color-bg-elevated);
		border: 2px dashed var(--color-border);
		border-radius: var(--radius-lg);
		opacity: 0.6;
	}

	/* Widgets fill their grid cell */
	.gridstack-container :global(.grid-stack-item-content .widget) {
		height: 100%;
		display: flex;
		flex-direction: column;
	}

	.gridstack-container :global(.grid-stack-item-content .widget-content) {
		flex: 1;
		overflow: auto;
	}

	/* App tiles fill their cell */
	.gridstack-container :global(.grid-stack-item-content .app-slot) {
		height: 100%;
	}

	/* Add widget floating bar */
	.add-widget-bar {
		display: flex;
		justify-content: flex-end;
		margin-top: var(--space-md);
	}

	.add-widget-btn {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		padding: var(--space-sm) var(--space-md);
		background: var(--color-bg-elevated);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-lg);
		color: var(--color-text-muted);
		font-family: var(--font-sans);
		font-size: 0.875rem;
		cursor: pointer;
		transition: border-color 0.15s ease, background 0.15s ease, color 0.15s ease;
	}

	.add-widget-btn:hover {
		border-color: var(--color-text-muted);
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.add-icon {
		font-size: 1.125rem;
		line-height: 1;
	}
</style>
