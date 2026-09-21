// SPDX-License-Identifier: AGPL-3.0-only
import { describe, it, expect } from 'vitest';
import { firstFreeSlot } from '../gridPlacement';
import type { GridNodeState } from '../gridDiff';

/** Build a placed item from `x,y,w,h` shorthand. */
function at(id: string, x: number, y: number, w: number, h: number): GridNodeState {
	return { id, x, y, w, h };
}

describe('firstFreeSlot', () => {
	it('uses the first cell of an empty grid', () => {
		expect(firstFreeSlot([], 1, 1, 0, 6)).toEqual({ x: 0, y: 0 });
	});

	it('starts scanning at fromY', () => {
		expect(firstFreeSlot([], 2, 2, 4, 6)).toEqual({ x: 0, y: 4 });
	});

	it('skips past a cell that is already taken', () => {
		expect(firstFreeSlot([at('a', 0, 0, 1, 1)], 1, 1, 0, 6)).toEqual({ x: 1, y: 0 });
	});

	it('wraps to the next row when the current one is full', () => {
		const full = [0, 1, 2, 3, 4, 5].map((x) => at(`a${x}`, x, 0, 1, 1));
		expect(firstFreeSlot(full, 1, 1, 0, 6)).toEqual({ x: 0, y: 1 });
	});

	it('wraps an item that does not fit in the remaining columns', () => {
		expect(firstFreeSlot([at('a', 0, 0, 4, 1)], 4, 2, 0, 6)).toEqual({ x: 0, y: 1 });
	});

	it('honours the height of items it is placing beside', () => {
		expect(firstFreeSlot([at('a', 0, 0, 1, 2)], 1, 2, 0, 6)).toEqual({ x: 1, y: 0 });
	});

	it('ignores items that start below fromY', () => {
		expect(firstFreeSlot([at('a', 0, 5, 6, 1)], 2, 2, 0, 6)).toEqual({ x: 0, y: 0 });
	});

	it('finds a gap left in an upper row rather than starting a new one', () => {
		const occupied = [at('a', 0, 0, 2, 1), at('b', 4, 0, 2, 1)];
		expect(firstFreeSlot(occupied, 2, 1, 0, 6)).toEqual({ x: 2, y: 0 });
	});
});
