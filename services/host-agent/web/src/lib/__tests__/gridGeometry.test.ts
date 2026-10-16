// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { GRID_BREAKPOINTS, GRID_CELL_HEIGHT, GRID_COLUMNS, GRID_GUTTER } from '../gridGeometry';

/**
 * The grid geometry has two readers, and only one of them can import it.
 *
 * GridStack positions its items from JavaScript, so it takes the constants. The
 * loading skeleton draws the same boxes in CSS before GridStack exists, so it
 * writes them down again. That is the drift this test closes: a skeleton that
 * promises six columns and gets four is worse than no skeleton, because the jump
 * is the defect the skeleton was added to remove.
 */

const SRC = new URL('../..', import.meta.url).pathname;
const gridSource = readFileSync(join(SRC, 'lib/components/GridStackGrid.svelte'), 'utf8');
const skeletonSource = readFileSync(join(SRC, 'lib/components/LoadingGrid.svelte'), 'utf8');

describe('GridStackGrid', () => {
	it('takes every dimension from the shared module', () => {
		expect(gridSource).toContain('GRID_COLUMNS');
		expect(gridSource).toContain('GRID_CELL_HEIGHT');
		expect(gridSource).toContain('GRID_GUTTER');
		expect(gridSource).toContain('GRID_BREAKPOINTS.map');
	});

	it('keeps no private copy of a breakpoint list', () => {
		expect(gridSource).not.toMatch(/breakpoints:\s*\[/);
	});
});

describe('LoadingGrid', () => {
	it('takes the gutter and the cell height from the shared module', () => {
		expect(skeletonSource).toContain('--grid-gutter: ${GRID_GUTTER}px');
		expect(skeletonSource).toContain('--grid-cell-height: ${GRID_CELL_HEIGHT}px');
		expect(skeletonSource).toContain('--grid-columns: ${GRID_COLUMNS}');
	});

	it('draws exactly the breakpoint set GridStack will switch at', () => {
		const rules = [...skeletonSource.matchAll(/@container \(max-width: (\d+)px\)\s*\{\s*\.loading-grid\s*\{\s*grid-template-columns:\s*repeat\((\d+), 1fr\);/g)];
		const drawn = rules.map((m) => ({ maxWidth: Number(m[1]), columns: Number(m[2]) }));
		expect(drawn).toEqual(GRID_BREAKPOINTS.map((b) => ({ maxWidth: b.maxWidth, columns: b.columns })));
	});

	it('measures its own box, the way GridStack does', () => {
		// A media query would read the viewport and disagree with the grid by the
		// width of the sidebar.
		expect(skeletonSource).toContain('container-type: inline-size');
		expect(skeletonSource).not.toMatch(/@media[^{]*\{[^}]*grid-template-columns/);
	});

	it('insets the row by half the gutter, the way GridStack insets a cell', () => {
		expect(skeletonSource).toContain('padding-inline: calc(var(--grid-gutter) / 2)');
	});
});

describe('GRID_COLUMNS', () => {
	it('is the six-column desktop grid the layout is authored at', () => {
		expect(GRID_COLUMNS).toBe(6);
		expect(GRID_CELL_HEIGHT).toBeGreaterThan(GRID_GUTTER);
	});
});
