// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Row primitives for the developer graph: the horizontal bands that sit above
 * the app group (connections) and below it (services), and the edge mapping.
 *
 * Split out of graphLayout.ts so that module keeps the box and group math.
 */

import type { Edge, Node } from '@xyflow/svelte';
import type { DeveloperGraph, GraphNode } from '$lib/clients/developerClient';
import { EDGE_STYLE, edgeKind, showsLabel } from '$lib/graph/edgeKinds';

export const NODE_WIDTH = 170;
export const NODE_HEIGHT = 60;
/** Horizontal gap between nodes within a row (connections, services). */
const CONN_HGAP = 60;

/** The left x of a row of `count` nodes, centered over the app group. */
export function rowStartX(groupX: number, groupWidth: number, count: number): number {
	const totalWidth = count * NODE_WIDTH + (count - 1) * CONN_HGAP;
	return groupX + groupWidth / 2 - totalWidth / 2;
}

/** Map `rowNodes` to a horizontal row starting at `baseX`, all at `y`. */
export function nodeRow(
	rowNodes: GraphNode[],
	baseX: number,
	y: number,
	dataFor: (n: GraphNode) => Record<string, unknown>
): Node[] {
	return rowNodes.map((cn, i) => ({
		id: cn.id,
		type: 'app',
		position: { x: baseX + i * (NODE_WIDTH + CONN_HGAP), y },
		data: dataFor(cn)
	}));
}

/**
 * Every edge of the graph.
 *
 * One style for all of them, and no animation: an edge that marches is a
 * constant motion in the middle of a picture whose whole job is to be still
 * while the state readout beside it changes.
 */
export function buildEdges(graph: DeveloperGraph): Edge[] {
	return graph.edges.map((e, i) => {
		const kind = edgeKind(e.label);
		return {
			id: `e-${i}`,
			source: e.source,
			target: e.target,
			label: showsLabel(kind) ? e.label : undefined,
			// xyflow/svelte takes these as style strings, not objects: the renderer
			// writes them straight into a `style` attribute.
			style: `stroke: ${EDGE_STYLE.stroke}; stroke-width: ${EDGE_STYLE.width}px;`,
			// The label is an HTML div portalled over the canvas (not SVG text), so
			// it can carry a real background. It sits on top of lines constantly, and
			// the canvas color behind the words is what punches the stroke out instead
			// of leaving a word sitting on a line it then cannot be read through.
			labelStyle:
				'font-size: 10px; color: #44403C; padding: 2px 6px; background: #FAF9F6; border-radius: 3px;',
		};
	});
}
