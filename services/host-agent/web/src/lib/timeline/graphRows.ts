// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Row primitives for the developer graph: the horizontal bands that sit above
 * the app group (connections) and below it (services), the "You" node that
 * hangs off the connection the operator actually reached through, and the edge
 * mapping that animates an edge when both of its endpoints are live.
 *
 * Split out of graphLayout.ts so that module keeps the box and group math.
 */

import type { Edge, Node } from '@xyflow/svelte';
import type { DeveloperGraph, GraphNode } from '$lib/clients/developerClient';

export const NODE_WIDTH = 170;
export const NODE_HEIGHT = 60;
export const USER_NODE_SIZE = 64;
/** Horizontal gap between nodes within a row (connections, services). */
const CONN_HGAP = 60;
const USER_GAP = 60;

/** The operator's own node, grafted onto the connection they reached through. */
export const YOU_ID = '__you__';

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

/**
 * The "You" node, centered above the connection at `userConnectionId`
 * (falls back to the first connection). Returns null when there is no user
 * connection or no connections at all.
 */
export function buildYouNode(
	connectionNodes: GraphNode[],
	userConnectionId: string | null,
	baseX: number,
	connY: number
): Node | null {
	if (!userConnectionId || connectionNodes.length === 0) return null;
	const found = connectionNodes.findIndex((cn) => cn.id === userConnectionId);
	const idx = found >= 0 ? found : 0;
	const connX = baseX + idx * (NODE_WIDTH + CONN_HGAP);
	return {
		id: YOU_ID,
		type: 'user',
		position: {
			x: connX + NODE_WIDTH / 2 - USER_NODE_SIZE / 2,
			y: connY - USER_NODE_SIZE - USER_GAP
		},
		data: { label: 'You', hasOutgoing: true }
	};
}

/** Every edge of the graph, animated when both of its endpoints are live. */
export function buildEdges(graph: DeveloperGraph, userConnectionId: string | null): Edge[] {
	const live = new Map(graph.nodes.map((n) => [n.id, isLiveNode(n)]));
	live.set(YOU_ID, true); // "You" is always live for animation
	const isLive = (id: string) => live.get(id) === true;

	const edges: Edge[] = graph.edges.map((e, i) => ({
		id: `e-${i}`,
		source: e.source,
		target: e.target,
		label: e.label,
		animated: isLive(e.source) && isLive(e.target)
	}));

	if (userConnectionId) {
		edges.push({
			id: 'e-you',
			source: YOU_ID,
			target: userConnectionId,
			animated: isLive(userConnectionId)
		});
	}
	return edges;
}
