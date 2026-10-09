// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Row primitives for the developer graph: the horizontal bands that sit above
 * the app group (connections) and below it (services), and the edge mapping
 * that animates an edge when both of its endpoints are live.
 *
 * Split out of graphLayout.ts so that module keeps the box and group math.
 */

import type { Edge, Node } from '@xyflow/svelte';
import type { DeveloperGraph, GraphNode } from '$lib/clients/developerClient';

export const NODE_WIDTH = 170;
export const NODE_HEIGHT = 60;
/** Horizontal gap between nodes within a row (connections, services). */
const CONN_HGAP = 60;

/** A status that counts as "live" for edge animation. */
function isActiveStatus(status: string): boolean {
	return status === 'running' || status === 'active';
}

/**
 * Whether one node counts as live for edge animation.
 *
 * An app or container is live when it is running. A `service` node is live
 * because the backend put it in the payload at all: the AI Model node carries
 * `external` rather than a lifecycle status precisely because nothing probes
 * it, and it disappears the moment Settings -> AI has no enabled upstream.
 * Its presence is the liveness claim. Reading that node as inert would draw the
 * one edge that is definitely wired as a dead line.
 */
function isLiveNode(n: GraphNode): boolean {
	return isActiveStatus(n.status) || n.nodeType === 'service';
}

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

/** Every edge of the graph, animated when both of its endpoints are live. */
export function buildEdges(graph: DeveloperGraph): Edge[] {
	const live = new Map(graph.nodes.map((n) => [n.id, isLiveNode(n)]));
	const isLive = (id: string) => live.get(id) === true;

	return graph.edges.map((e, i) => ({
		id: `e-${i}`,
		source: e.source,
		target: e.target,
		label: e.label,
		animated: isLive(e.source) && isLive(e.target)
	}));
}
