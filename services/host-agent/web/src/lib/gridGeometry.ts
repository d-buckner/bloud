// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The home grid's geometry, in one place.
 *
 * GridStack needs these as numbers in JavaScript: it positions every item itself
 * and has no way to read a stylesheet. The skeleton has to draw the same boxes
 * before GridStack exists. So the numbers live here, and both sides read them,
 * which is what keeps a skeleton from promising a layout the grid will not keep.
 */

/** Columns the layout is authored at. Narrower viewports get fewer as a
 * read-only preview. */
export const GRID_COLUMNS = 6;

/** Row height in px, so a 1x1 app tile is one cell. */
export const GRID_CELL_HEIGHT = 128;

/** Gap between tiles in px. GridStack insets each cell by half of it. */
export const GRID_GUTTER = 12;

/**
 * Column counts at container widths, largest width first.
 *
 * GridStack matches these against the width of its own element, not the
 * viewport, which is why the skeleton uses a container query rather than a media
 * query. `LoadingGrid.svelte` writes the same numbers in CSS, and
 * `gridGeometry.test.ts` fails the build when the two lists drift apart.
 */
export const GRID_BREAKPOINTS = [
	{ maxWidth: 820, columns: 4 },
	{ maxWidth: 560, columns: 2 }
] as const;
