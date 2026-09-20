// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
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
 * Always terminates: once every placed item is above, the row below them is
 * empty by construction.
 */
export function firstFreeSlot(
	occupied: GridNodeState[],
	w: number,
	h: number,
	fromY: number,
	columns: number
): GridSlot {
	for (let y = fromY; ; y++) {
		for (let x = 0; x + w <= columns; x++) {
			// Reject a slot that shares any cell with a placed item.
			const free = !occupied.some(
				(o) => o.x < x + w && o.x + o.w > x && o.y < y + h && o.y + o.h > y
			);
			if (free) return { x, y };
		}
	}
}
