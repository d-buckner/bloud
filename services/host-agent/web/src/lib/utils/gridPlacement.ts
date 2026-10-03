// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Grid placement: where a newly added element goes. Pure (no GridStack
 * instance), so the free-slot scan is unit-testable.
 */

import type { GridNodeState } from './gridDiff';

export interface GridSlot {
	x: number;
	y: number;
}

/**
 * First free slot for a `w`×`h` element at or below `fromY`, scanning rows
 * top-to-bottom and each row left-to-right.
 *
 * The scan is bounded by the row below the lowest placed item. At `y = limit`
 * every item satisfies `o.y + o.h <= y`, so the overlap test cannot fire and
 * that row is free by construction: the bound is always enough, and a scan that
 * reaches its end without returning means the grid itself is degenerate. Bounded
 * this way, a wrong overlap test is a wrong slot, but an impossible request is a
 * thrown Error rather than a browser that stops responding.
 *
 * @throws if `columns` is less than 1, which leaves no cell to place anything in.
 */
export function firstFreeSlot(
	occupied: GridNodeState[],
	w: number,
	h: number,
	fromY: number,
	columns: number
): GridSlot {
	// The caller's width comes from the widget registry, which is authored at
	// desktop width; the viewport can be narrower than that, so never scan for a
	// slot wider than the grid has.
	const width = Math.max(1, Math.min(w, columns));
	const limit = occupied.reduce((bottom, o) => Math.max(bottom, o.y + o.h), fromY);
	for (let y = fromY; y <= limit; y++) {
		for (let x = 0; x + width <= columns; x++) {
			// Reject a slot that shares any cell with a placed item.
			const free = !occupied.some(
				(o) => o.x < x + width && o.x + o.w > x && o.y < y + h && o.y + o.h > y
			);
			if (free) return { x, y };
		}
	}
	throw new Error(`no ${width}-wide slot in a ${columns}-column grid`);
}
