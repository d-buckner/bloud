// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { diffGrid, type GridNodeState } from '../gridDiff';
import type { GridElement } from '$lib/types';

const el = (id: string, over: Partial<GridElement> = {}): GridElement => ({
	type: 'app',
	id,
	x: 0,
	y: 0,
	w: 1,
	h: 1,
	...over
});
const ns = (id: string, over: Partial<GridNodeState> = {}): GridNodeState => ({
	id,
	x: 0,
	y: 0,
	w: 1,
	h: 1,
	...over
});

const ids = (els: { id: string }[]) => els.map((e) => e.id);

describe('diffGrid', () => {
	it('removes grid nodes absent from the store', () => {
		const d = diffGrid([ns('keep'), ns('gone')], [el('keep')], false);
		expect(d.remove).toEqual(['gone']);
		expect(d.add).toEqual([]);
		expect(d.structural).toBe(true);
	});

	it('adds store elements not yet on the grid', () => {
		const d = diffGrid([ns('a')], [el('a'), el('b')], false);
		expect(ids(d.add)).toEqual(['b']);
		expect(d.remove).toEqual([]);
		expect(d.structural).toBe(true);
	});

	it('a fully matching state produces no changes and is not structural', () => {
		const d = diffGrid([ns('a', { x: 2, y: 3, w: 2, h: 2 })], [el('a', { x: 2, y: 3, w: 2, h: 2 })], false);
		expect(d).toMatchObject({ remove: [], update: [], structural: false });
	});

	it('updates an existing element whose geometry changed', () => {
		const d = diffGrid([ns('a', { x: 0, y: 0, w: 1, h: 1 })], [el('a', { x: 5, y: 0, w: 1, h: 1 })], false);
		expect(ids(d.update)).toEqual(['a']);
		expect(d.update[0]).toMatchObject({ x: 5 });
		expect(d.structural).toBe(false); // position-only change is not structural
	});

	it('resizing triggers an update too', () => {
		const d = diffGrid([ns('a', { w: 1, h: 1 })], [el('a', { w: 3, h: 1 })], false);
		expect(ids(d.update)).toEqual(['a']);
	});

	it('does not update while the user is dragging', () => {
		const d = diffGrid([ns('a', { x: 0 })], [el('a', { x: 9 })], true);
		expect(d.update).toEqual([]);
	});

	it('skips a positioned-in-store item that is present but null-coordinate', () => {
		const d = diffGrid([ns('a', { x: 0 })], [el('a', { x: null, y: null })], false);
		expect(d.update).toEqual([]);
	});

	it('still adds/removes structurally while dragging (only updates are suppressed)', () => {
		const d = diffGrid([ns('gone')], [el('fresh')], true);
		expect(d.remove).toEqual(['gone']);
		expect(ids(d.add)).toEqual(['fresh']);
		expect(d.update).toEqual([]);
		expect(d.structural).toBe(true);
	});
});
