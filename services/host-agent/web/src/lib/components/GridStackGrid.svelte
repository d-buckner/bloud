<script lang="ts">
	import { onMount, onDestroy, mount, unmount } from 'svelte';
	import { GridStack, type GridStackNode } from 'gridstack';
	import { layout, type GridElement } from '$lib/stores/layout';
	import { getWidgetById } from '$lib/widgets/registry';
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

	// Prevent feedback loop: when we update the store from grid events,
	// the $effect watching $layout would call syncGridFromStore again.
	let suppressStoreSync = false;

	/**
	 * Add a single GridStack item and mount its Svelte component into it.
	 */
	function addGridStackItem(element: GridElement) {
		const isApp = element.type === 'app';
		const widget = isApp ? null : getWidgetById(element.id);

		const node = grid.addWidget({
			id: element.id,
			x: element.x,
			y: element.y,
			w: element.w,
			h: element.h,
			noResize: isApp,
			// Widgets use their header as drag handle (set via handleClass below)
			noMove: false,
			minW: isApp ? 1 : (widget?.size.cols ?? 1),
			minH: isApp ? 1 : (widget?.size.rows ?? 1),
		});

		// Find the content div GridStack created for this item
		const contentEl = node?.querySelector('.grid-stack-item-content');
		if (!contentEl) return;

		// Mount the appropriate Svelte component into the content div
		let instance: ReturnType<typeof mount>;
		if (isApp) {
			instance = mount(AppTile, {
				target: contentEl as HTMLElement,
				props: {
					itemId: element.id,
					onAppClick,
					onAppContextMenu,
				},
			});
		} else if (widget) {
			instance = mount(WidgetWrapper, {
				target: contentEl as HTMLElement,
				props: {
					widget,
					onRemove: () => {
						layout.removeWidget(element.id);
					},
				},
			});
		} else {
			return;
		}

		mountedComponents.set(element.id, instance);
	}

	/**
	 * Sync the GridStack DOM to match the store's element list.
	 * Called on mount and whenever $layout changes externally.
	 */
	function syncGridFromStore(elements: GridElement[]) {
		if (!grid) return;

		const storeIds = new Set(elements.map((e) => e.id));

		grid.batchUpdate(true);

		// Remove GridStack items that are no longer in the store
		const existingNodes = grid.getGridItems();
		for (const node of existingNodes) {
			const id = node.gridstackNode?.id as string | undefined;
			if (id && !storeIds.has(id)) {
				// Unmount Svelte component first
				const instance = mountedComponents.get(id);
				if (instance) {
					unmount(instance);
					mountedComponents.delete(id);
				}
				grid.removeWidget(node, true, false);
			}
		}

		// Get current GridStack item ids after removal
		const currentIds = new Set(
			grid.getGridItems()
				.map((n) => n.gridstackNode?.id as string | undefined)
				.filter(Boolean)
		);

		// Add items that are in the store but not yet in GridStack
		for (const element of elements) {
			if (!currentIds.has(element.id)) {
				addGridStackItem(element);
			} else {
				// Update position/size for existing items if they differ
				const node = grid.getGridItems().find(
					(n) => (n.gridstackNode?.id as string) === element.id
				);
				if (node?.gridstackNode) {
					const gsn = node.gridstackNode;
					if (
						gsn.x !== element.x ||
						gsn.y !== element.y ||
						gsn.w !== element.w ||
						gsn.h !== element.h
					) {
						grid.update(node, { x: element.x, y: element.y, w: element.w, h: element.h });
					}
				}
			}
		}

		grid.batchUpdate(false);
	}

	/**
	 * Called by GridStack on any drag/resize change event.
	 * Reads the current node positions and writes back to the store.
	 */
	function handleGridChange(_event: Event, nodes: GridStackNode[]) {
		if (suppressStoreSync) return;
		if (!nodes || nodes.length === 0) return;

		// Build updated elements list preserving type info
		const currentStore = $layout; // snapshot
		const storeMap = new Map(currentStore.map((e) => [e.id, e]));

		const newElements = grid.getGridItems().map((item) => {
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

		suppressStoreSync = true;
		layout.setElements(newElements);
		suppressStoreSync = false;
	}

	onMount(() => {
		grid = GridStack.init(
			{
				column: 6,
				cellHeight: 100,
				margin: 8,
				float: false,
				animate: true,
				// Widget headers are the drag handle; app tiles drag via the entire cell
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

		// Initial population from store
		syncGridFromStore($layout);

		// Listen for drag/resize changes
		grid.on('change', handleGridChange);
	});

	// React to external store changes (widget picker toggle, app install/uninstall)
	$effect(() => {
		const elements = $layout;
		if (!suppressStoreSync) {
			syncGridFromStore(elements);
		}
	});

	onDestroy(() => {
		// Unmount all Svelte components
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
