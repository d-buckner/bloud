// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Grid layout diff: pure comparison between the store's element list and the
 * grid's current node state. Extracted from GridStackGrid so the
 * remove/add/update/skip rules are unit-testable without a GridStack instance;
 * the component turns the returned plan into DOM operations.
 */

import type { GridElement } from '$lib/types';

/** A grid item's current geometry as reported by GridStack. */
export interface GridNodeState {
	id: string;
	x: number;
	y: number;
	w: number;
	h: number;
}

export interface GridDiff {
	/** Store items no longer present in the store (ids): remove from grid. */
	remove: string[];
	/** Store items not yet on the grid: add (autoPosition when x/y null). */
	add: GridElement[];
	/** Existing items whose geometry changed: move/resize to the store's. */
	update: GridElement[];
	/** True when membership changed (add or remove), i.e. a layout PUT is due. */
	structural: boolean;
}

/** Whether an existing node's geometry differs from the store element's. */
function geometryChanged(cur: GridNodeState, el: GridElement): boolean {
	return cur.x !== el.x || cur.y !== el.y || cur.w !== el.w || cur.h !== el.h;
}

/**
 * Compute the diff between the grid's current state and the store elements.
 *
 *  - remove: on the grid but not in the store
 *  - add: in the store but not on the grid
 *  - update: on both, has concrete coordinates, geometry differs
 *  - skip: dragging (don't fight the user), or a stored item with no
 *    coordinates yet (nothing to move to)
 */
export function diffGrid(
	current: GridNodeState[],
	elements: GridElement[],
	isDragging: boolean
): GridDiff {
	const storeIds = new Set(elements.map((e) => e.id));
	const currentById = new Map(current.map((n) => [n.id, n]));

	const remove = current.filter((n) => !storeIds.has(n.id)).map((n) => n.id);
	const add: GridElement[] = [];
	const update: GridElement[] = [];

	for (const el of elements) {
		const cur = currentById.get(el.id);
		if (!cur) {
			add.push(el);
			continue;
		}
		if (isDragging || el.x === null || el.y === null) continue;
		if (geometryChanged(cur, el)) update.push(el);
	}

	return { remove, add, update, structural: remove.length > 0 || add.length > 0 };
}
