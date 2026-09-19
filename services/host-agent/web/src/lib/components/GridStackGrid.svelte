<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import { onMount, onDestroy, mount, unmount } from 'svelte';
	import { SvelteMap } from 'svelte/reactivity';
	import { GridStack } from 'gridstack';
	import { gridElements, type GridElement } from '$lib/stores/grid';
	import { getWidgetById } from '$lib/widgets/registry';
	import { saveLayout } from '$lib/clients/layoutClient';
	import { type App } from '$lib/types';
	import AppTile from './AppTile.svelte';
	import WidgetWrapper from './WidgetWrapper.svelte';
	import { diffGrid, type GridDiff, type GridNodeState } from '$lib/utils/gridDiff';
	import { firstFreeSlot } from '$lib/utils/gridPlacement';

	interface Props {
		onAppClick?: (app: App) => void;
		onAppContextMenu?: (e: MouseEvent, app: App) => void;
	}

	let { onAppClick, onAppContextMenu }: Props = $props();

	/**
	 * Columns the layout is authored at. Narrower viewports get fewer columns
	 * as a read-only preview — see handleGridChange.
	 */
	const DESKTOP_COLUMNS = 6;
	/** Row height in px, so a 1x1 app tile is one cell. */
	const CELL_HEIGHT = 128;
	/**
	 * Gap between tiles in px. Mirrored into `--grid-gutter` for the container,
	 * which cancels the outer margins so tiles align with the page content.
	 */
	const CELL_MARGIN = 12;
	/**
	 * GridStack moves the dragged element under the cursor, so releasing a
	 * drag still delivers a click on it. Swallow that one click.
	 */
	const CLICK_GUARD_MS = 250;

	let gridEl: HTMLElement;
	let grid: GridStack;
	let columns = $state(DESKTOP_COLUMNS);

	// Track mounted Svelte component instances by item id for cleanup
	const mountedComponents = new SvelteMap<string, ReturnType<typeof mount>>();

	// Prevent sending PUT requests for programmatic position syncs.
	// True while syncGridFromStore is running; false during user drag/resize.
	let suppressLayoutSave = false;

	// Prevent syncGridFromStore from interrupting an active user drag.
	let isDragging = false;

	let clickGuardUntil = 0;

	/** Watches the container so a viewport change is reconciled promptly. */
	let resizeObserver: ResizeObserver | undefined;
	let reconcileTimer: ReturnType<typeof setTimeout> | undefined;

	/** Drop the click that immediately follows a drag of the same element. */
	function handleAppClick(app: App) {
		if (Date.now() < clickGuardUntil) return;
		onAppClick?.(app);
	}

	/**
	 * Mount the Svelte component for a grid item into its content element.
	 * Returns null for a non-app element with no matching widget (nothing to render).
	 */
	function mountComponentFor(
		element: GridElement,
		target: HTMLElement,
		widget: ReturnType<typeof getWidgetById>
	): ReturnType<typeof mount> | null {
		if (element.type === 'app') {
			return mount(AppTile, {
				target,
				props: { itemId: element.id, onAppClick: handleAppClick, onAppContextMenu },
			});
		}
		if (!widget) return null;
		return mount(WidgetWrapper, {
			target,
			props: { widget, onRemove: () => gridElements.removeWidget(element.id) },
		});
	}

	/**
	 * Row where widgets start: below the app tiles, so a fresh widget joins the
	 * widget block instead of filling the first gap among the apps.
	 */
	function appBlockBottom(occupied: GridNodeState[]): number {
		const appIds = new Set($gridElements.filter((el) => el.type === 'app').map((el) => el.id));
		return occupied
			.filter((node) => appIds.has(node.id))
			.reduce((bottom, node) => Math.max(bottom, node.y + node.h), 0);
	}

	/**
	 * Where a newly added element goes: a stored position always wins, an app
	 * fills the first free cell, and a widget opens the block below the apps.
	 */
	function placementFor(element: GridElement, size: { w: number; h: number }) {
		if (element.x !== null && element.y !== null) {
			return { x: element.x, y: element.y };
		}
		if (element.type === 'app') return { autoPosition: true as const };

		const occupied = currentGridState();
		const slot = firstFreeSlot(occupied, size.w, size.h, appBlockBottom(occupied), columns);
		return { x: slot.x, y: slot.y };
	}

	/**
	 * Size constraints for an item: app tiles are a fixed cell, widgets take
	 * their registry default as the minimum and `maxSize` as the ceiling.
	 */
	function sizeConstraints(element: GridElement) {
		const widget = element.type === 'widget' ? getWidgetById(element.id) : undefined;
		if (!widget) {
			return { minW: 1, minH: 1, maxW: 1, maxH: 1, w: 1, h: 1, noResize: true };
		}
		const { cols: minW, rows: minH } = widget.size;
		const { cols: maxW, rows: maxH } = widget.maxSize;
		return {
			minW,
			minH,
			maxW,
			maxH,
			w: Math.min(Math.max(element.w, minW), maxW),
			h: Math.min(Math.max(element.h, minH), maxH),
			noResize: false,
		};
	}

	/**
	 * Add a single GridStack item and mount its Svelte component into it.
	 */
	function addGridStackItem(element: GridElement) {
		const widget = element.type === 'widget' ? getWidgetById(element.id) : undefined;
		const size = sizeConstraints(element);

		const node = grid.addWidget({
			id: element.id,
			...placementFor(element, size),
			...size,
			noMove: false,
		});

		const contentEl = node?.querySelector('.grid-stack-item-content');
		if (!contentEl) return;

		const instance = mountComponentFor(element, contentEl as HTMLElement, widget);
		if (!instance) return;
		mountedComponents.set(element.id, instance);
	}

	/** Snapshot the grid's current node geometries for diffing against the store. */
	function currentGridState(): GridNodeState[] {
		const state: GridNodeState[] = [];
		for (const item of grid.getGridItems()) {
			const gsn = item.gridstackNode;
			if (!gsn?.id) continue;
			state.push({
				id: gsn.id as string,
				x: gsn.x ?? 0,
				y: gsn.y ?? 0,
				w: gsn.w ?? 1,
				h: gsn.h ?? 1,
			});
		}
		return state;
	}

	/** Locate a grid item element by its store id. */
	function findNodeById(id: string) {
		return grid.getGridItems().find((n) => (n.gridstackNode?.id as string) === id);
	}

	/**
	 * Fit a stored position into the columns on screen. At desktop width this
	 * is the identity; on a narrower preview it keeps items from being placed
	 * past the last column.
	 */
	function fitToColumns(element: GridElement) {
		const maxX = Math.max(0, columns - Math.min(element.w, columns));
		return {
			x: Math.min(Math.max(element.x ?? 0, 0), maxX),
			y: element.y ?? 0,
			w: Math.min(element.w, columns),
			h: element.h,
		};
	}

	/**
	 * Tear down one grid item: unmount its Svelte component and remove the DOM
	 * node without letting GridStack emit its own removal events.
	 */
	function removeGridItem(id: string) {
		const node = findNodeById(id);
		if (!node) return;

		const instance = mountedComponents.get(id);
		if (instance) {
			unmount(instance);
			mountedComponents.delete(id);
		}
		grid.removeWidget(node, true, false);
	}

	/** Persist the grid exactly as it currently stands as the user's layout. */
	function persistLayout() {
		if (suppressLayoutSave) return;

		const storeMap = new Map($gridElements.map((e) => [e.id, e]));
		const settled = currentGridState().map((node) => {
			const existing = storeMap.get(node.id);
			return {
				type: existing?.type ?? 'app',
				id: node.id,
				x: node.x,
				y: node.y,
				w: node.w,
				h: node.h,
			} satisfies GridElement;
		});

		if (settled.length === 0) return;
		saveLayout(settled);
	}

	/**
	 * Whether this diff is only the stored layout being drawn for the first
	 * time — an empty grid where every element already carries a position —
	 * rather than a change worth saving back.
	 */
	function isReplayingStoredLayout(diff: GridDiff, elements: GridElement[]): boolean {
		if (diff.remove.length > 0 || diff.add.length !== elements.length) return false;
		return elements.every((el) => el.x !== null && el.y !== null);
	}

	/**
	 * Sync the GridStack DOM to match the provided element list.
	 * The add/remove/update/skip decision lives in `diffGrid` (unit-tested);
	 * this applies it to the DOM.
	 *
	 * suppressLayoutSave stays true across the programmatic changes so they
	 * don't fire a PUT mid-sync.
	 */
	function syncGridFromStore(elements: GridElement[]) {
		if (!grid) return;

		const diff = diffGrid(currentGridState(), elements, isDragging);

		suppressLayoutSave = true;
		grid.batchUpdate(true);

		diff.remove.forEach(removeGridItem);

		for (const element of diff.add) {
			addGridStackItem(element);
		}

		for (const element of diff.update) {
			const node = findNodeById(element.id);
			if (!node) continue;
			grid.update(node, fitToColumns(element));
		}

		grid.batchUpdate(false);
		suppressLayoutSave = false;

		// A structural change (install, uninstall, widget toggle) *is* the new
		// layout. GridStack announces it only via a 'change' event, and it
		// swallows that while a batch is open — so persist it here rather than
		// waiting for the event that never comes.
		// A narrow viewport is a preview of the desktop layout: never persist.
		const persist = diff.structural && !isReplayingStoredLayout(diff, elements);
		if (persist && columns === DESKTOP_COLUMNS) persistLayout();
	}

	/**
	 * Dense reading-order layout for a narrow (preview) viewport. Compressing
	 * the stored desktop positions into fewer columns leaves holes and pushes
	 * items down, so the preview is laid out from scratch instead — and it is
	 * never saved back.
	 */
	function previewElements(elements: GridElement[]): GridElement[] {
		// Sort by the desktop layout's reading order: the home snapshot's
		// element order is not guaranteed stable, and a preview that reshuffles
		// on its own would be worse than a compressed one.
		const readingOrder = (el: GridElement) => (el.y ?? Number.MAX_SAFE_INTEGER) * 1000 + (el.x ?? 0);

		const placed: GridNodeState[] = [];
		return [...elements]
			.sort((a, b) => readingOrder(a) - readingOrder(b))
			.map((element) => {
				const w = Math.min(element.w, columns);
				const slot = firstFreeSlot(placed, w, element.h, 0, columns);
				placed.push({ id: element.id, x: slot.x, y: slot.y, w, h: element.h });
				return { ...element, x: slot.x, y: slot.y, w };
			});
	}

	/**
	 * Bring the grid into line with the viewport and the store: adopt whatever
	 * column count GridStack settled on, gate the drag affordances on it, and
	 * lay the elements out.
	 *
	 * The column count is *observed* rather than taken from a 'change' event,
	 * because GridStack raises that event from inside its own resize batch and
	 * swallows it (the same way it swallows the event for programmatic adds).
	 */
	function reconcile(elements: GridElement[]) {
		if (!grid) return;

		const next = grid.getColumn();
		if (next !== columns) {
			columns = next;
			const editable = columns === DESKTOP_COLUMNS;
			grid.enableMove(editable);
			grid.enableResize(editable);
		}

		// The layout is authored at desktop width; anything narrower is a
		// read-only preview that is re-derived rather than scaled, so
		// narrowing and widening again cannot drift.
		syncGridFromStore(columns === DESKTOP_COLUMNS ? elements : previewElements(elements));
	}

	/**
	 * Coalesce a burst of container resizes into one reconcile, after GridStack
	 * has finished its own resize handling for the same frame.
	 */
	function scheduleReconcile() {
		clearTimeout(reconcileTimer);
		reconcileTimer = setTimeout(() => {
			reconcileTimer = undefined;
			reconcile($gridElements);
		}, 0);
	}

	/** Called by GridStack on any drag/resize change event. */
	function handleGridChange() {
		if (!grid) return;
		if (grid.getColumn() !== columns) {
			scheduleReconcile();
			return;
		}
		if (columns !== DESKTOP_COLUMNS) return;
		persistLayout();
	}

	onMount(() => {
		grid = GridStack.init(
			{
				column: DESKTOP_COLUMNS,
				cellHeight: CELL_HEIGHT,
				margin: CELL_MARGIN,
				float: false,
				animate: true,
				handleClass: 'grid-drag-handle',
				columnOpts: {
					// Without this the responsive fallback column count is 12,
					// not the configured `column`.
					columnMax: DESKTOP_COLUMNS,
					breakpointForWindow: false,
					breakpoints: [
						{ w: 560, c: 2 },
						{ w: 820, c: 4 },
					],
				},
			},
			gridEl
		);

		// Track drag state so syncGridFromStore doesn't interrupt the user,
		// and so the click GridStack leaves behind doesn't open the app.
		grid.on('dragstart resizestart', () => {
			isDragging = true;
		});
		grid.on('dragstop resizestop', () => {
			isDragging = false;
			clickGuardUntil = Date.now() + CLICK_GUARD_MS;
		});

		grid.on('change', handleGridChange);

		// A narrow first paint already picked a smaller column count, and any
		// later container resize is picked up here rather than from a
		// GridStack event.
		reconcile($gridElements);
		resizeObserver = new ResizeObserver(scheduleReconcile);
		resizeObserver.observe(gridEl.parentElement ?? gridEl);
	});

	// React to store changes (snapshot, widget toggle, app install/uninstall)
	$effect(() => {
		reconcile($gridElements);
	});

	onDestroy(() => {
		for (const [, instance] of mountedComponents) {
			unmount(instance);
		}
		mountedComponents.clear();

		clearTimeout(reconcileTimer);
		resizeObserver?.disconnect();

		if (grid) {
			grid.off('change');
			grid.destroy(false);
		}
	});
</script>

<div
	class="gridstack-container"
	class:preview={columns !== DESKTOP_COLUMNS}
	style="--grid-gutter: {CELL_MARGIN}px"
>
	<div class="grid-stack" bind:this={gridEl}></div>
</div>

<style>
	.gridstack-container {
		/* GridStack insets every item by the gutter; pulling the container out
		   by the same amount lines the outer tiles up with the page content. */
		width: calc(100% + 2 * var(--grid-gutter));
		margin: calc(-1 * var(--grid-gutter)) calc(-1 * var(--grid-gutter)) 0;
	}

	/* Our components supply their own surfaces */
	.gridstack-container :global(.grid-stack-item-content) {
		background: transparent;
		border-radius: var(--radius-lg);
		overflow: visible;
	}

	/* Drag/resize affordance: the whole tile or the widget header moves */
	.gridstack-container :global(.grid-drag-handle) {
		cursor: grab;
	}

	.gridstack-container :global(.grid-stack-item.ui-draggable-dragging .grid-drag-handle),
	.gridstack-container :global(.grid-stack-item.ui-resizable-resizing .grid-drag-handle) {
		cursor: grabbing;
	}

	.gridstack-container :global(.grid-stack-item.ui-draggable-dragging) {
		z-index: 10;
	}

	/* The dragged tile floats above its slot */
	.gridstack-container :global(.grid-stack-item.ui-draggable-dragging .app-tile),
	.gridstack-container :global(.grid-stack-item.ui-draggable-dragging .widget) {
		box-shadow: var(--shadow-md), 0 12px 28px rgba(28, 25, 23, 0.14);
		border-color: var(--color-text-muted);
	}

	/* Drop target */
	.gridstack-container :global(.grid-stack-placeholder > .placeholder-content) {
		background: var(--color-bg-subtle);
		border: 1px dashed var(--color-text-muted);
		border-radius: var(--radius-lg);
	}

	/* Resize grip — hidden until the widget is hovered (gridstack autohide) */
	.gridstack-container :global(.grid-stack-item > .ui-resizable-se) {
		width: 26px;
		height: 26px;
		bottom: 0;
		right: 0;
		background-image: none;
		opacity: 0.45;
	}

	.gridstack-container :global(.grid-stack-item > .ui-resizable-se::after) {
		content: '';
		position: absolute;
		right: 9px;
		bottom: 9px;
		width: 6px;
		height: 6px;
		border-right: 1.5px solid var(--color-text-secondary);
		border-bottom: 1.5px solid var(--color-text-secondary);
		border-radius: 0 0 2px 0;
	}

	.gridstack-container :global(.grid-stack-item:hover > .ui-resizable-se) {
		opacity: 1;
	}

	/* Widgets fill their cell (app tiles size themselves) */
	.gridstack-container :global(.grid-stack-item-content > .widget) {
		height: 100%;
	}

	/* Read-only preview at narrow viewports */
	.gridstack-container.preview :global(.grid-drag-handle) {
		cursor: default;
	}
</style>
