// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * Developer graph layout — pure mapping from a DeveloperGraph to xyflow
 * Node/Edge arrays. Extracted from the developer page so the coordinate math
 * is unit-testable. The only environment dependency (the browser hostname used
 * to decide which connection the operator reaches through) is injected as a
 * `hostname` argument instead of read from `window`.
 *
 * The layout is: connections in a row above the app group; a "You" node above
 * the specific connection the operator is using; apps inside a bounding group
 * positioned by dagre. When there are no apps the connection row sits at the
 * origin. Node/edge animation reflects whether endpoints are 'running'/'active'.
 */

import dagre from '@dagrejs/dagre';
import type { Edge, Node } from '@xyflow/svelte';
import type { DeveloperGraph, GraphNode } from '$lib/clients/developerClient';

export const NODE_WIDTH = 170;
export const NODE_HEIGHT = 60;
export const USER_NODE_SIZE = 64;
const GROUP_PADDING = 40;
const CONNECTION_GAP = 100;
const USER_GAP = 60;
/** Horizontal gap between connection nodes in the row. */
const CONN_HGAP = 60;

const YOU_ID = '__you__';
const APPS_GROUP_ID = '__apps_group';

/** A status that counts as "live" for edge animation. */
function isActiveStatus(status: string): boolean {
	return status === 'running' || status === 'active';
}

/**
 * Which connection node the operator is reaching the host through: the tailnet
 * connection when `hostname` falls under the tailnet domain, else the LAN
 * connection, else none.
 */
export function detectUserConnection(graph: DeveloperGraph, hostname: string): string | null {
	const tailnetDomain = graph.tailnetDomain;
	if (tailnetDomain && hostname.endsWith(tailnetDomain)) {
		const tailnetConn = graph.nodes.find((n) => n.id.startsWith('conn:tailnet:'));
		if (tailnetConn) return tailnetConn.id;
	}
	const localConn = graph.nodes.find((n) => n.id === 'conn:local');
	return localConn ? localConn.id : null;
}

/** Run dagre over the app subgraph and return the axis-aligned bounds. */
function layoutAppBounds(appNodes: GraphNode[], appEdges: { source: string; target: string }[]) {
	const g = new dagre.graphlib.Graph();
	g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 });
	g.setDefaultEdgeLabel(() => ({}));
	for (const n of appNodes) g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT });
	for (const e of appEdges) g.setEdge(e.source, e.target);
	dagre.layout(g);

	let minX = Infinity;
	let minY = Infinity;
	let maxX = -Infinity;
	let maxY = -Infinity;
	for (const n of appNodes) {
		const pos = g.node(n.id);
		minX = Math.min(minX, pos.x - NODE_WIDTH / 2);
		minY = Math.min(minY, pos.y - NODE_HEIGHT / 2);
		maxX = Math.max(maxX, pos.x + NODE_WIDTH / 2);
		maxY = Math.max(maxY, pos.y + NODE_HEIGHT / 2);
	}
	return { g, minX, minY, maxX, maxY };
}

/** The connection row's left x, centered over the app group. */
function connectionStartX(groupX: number, groupWidth: number, count: number): number {
	const totalWidth = count * NODE_WIDTH + (count - 1) * CONN_HGAP;
	return groupX + groupWidth / 2 - totalWidth / 2;
}

/** Map connection nodes to a horizontal row starting at `baseX`, all at `y`. */
function connectionRow(
	connectionNodes: GraphNode[],
	baseX: number,
	y: number,
	dataFor: (n: GraphNode) => Record<string, unknown>
): Node[] {
	return connectionNodes.map((cn, i) => ({
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
function buildYouNode(
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

/** Build edges, animating those whose endpoints are both active. */
function buildEdges(graph: DeveloperGraph, userConnectionId: string | null): Edge[] {
	const statusById = new Map(graph.nodes.map((n) => [n.id, n.status]));
	statusById.set(YOU_ID, 'active'); // "You" is always live for animation

	const edges: Edge[] = graph.edges.map((e, i) => ({
		id: `e-${i}`,
		source: e.source,
		target: e.target,
		label: e.label,
		animated: isActiveStatus(statusById.get(e.source) ?? '') && isActiveStatus(statusById.get(e.target) ?? '')
	}));

	if (userConnectionId) {
		edges.push({
			id: 'e-you',
			source: YOU_ID,
			target: userConnectionId,
			animated: isActiveStatus(statusById.get(userConnectionId) ?? '')
		});
	}
	return edges;
}

/** Lay the whole developer graph out to xyflow nodes + edges. */
export function layoutGraph(graph: DeveloperGraph, hostname: string): { nodes: Node[]; edges: Edge[] } {
	const appNodes = graph.nodes.filter((n) => n.nodeType === 'app');
	const connectionNodes = graph.nodes.filter((n) => n.nodeType === 'connection');

	const appNodeIds = new Set(appNodes.map((n) => n.id));
	const appEdges = graph.edges.filter((e) => appNodeIds.has(e.source) && appNodeIds.has(e.target));
	const bounds = layoutAppBounds(appNodes, appEdges);

	const sources = new Set(graph.edges.map((e) => e.source));
	const targets = new Set(graph.edges.map((e) => e.target));
	const userConnectionId = detectUserConnection(graph, hostname);
	if (userConnectionId) {
		sources.add(YOU_ID);
		targets.add(userConnectionId);
	}
	const dataFor = (n: GraphNode) => ({
		displayName: n.displayName,
		status: n.status,
		isSystem: n.isSystem,
		nodeType: n.nodeType,
		hasOutgoing: sources.has(n.id),
		hasIncoming: targets.has(n.id)
	});

	const nodes: Node[] = [];
	// Connection row anchor: set by whichever layout branch runs, reused for
	// the "You" node so it lands above the correct connection.
	let connBaseX = 0;
	let connY = 0;
	if (appNodes.length > 0) {
		const groupWidth = bounds.maxX - bounds.minX + GROUP_PADDING * 2;
		const groupHeight = bounds.maxY - bounds.minY + GROUP_PADDING * 2;
		const groupX = bounds.minX - GROUP_PADDING;
		const groupY = bounds.minY - GROUP_PADDING;

		nodes.push({
			id: APPS_GROUP_ID,
			type: 'group',
			position: { x: groupX, y: groupY },
			style: `width: ${groupWidth}px; height: ${groupHeight}px;`,
			data: {}
		});
		for (const n of appNodes) {
			const pos = bounds.g.node(n.id);
			nodes.push({
				id: n.id,
				type: 'app',
				position: { x: pos.x - NODE_WIDTH / 2 - groupX, y: pos.y - NODE_HEIGHT / 2 - groupY },
				parentId: APPS_GROUP_ID,
				data: dataFor(n)
			});
		}
		connY = groupY - NODE_HEIGHT - CONNECTION_GAP;
		connBaseX = connectionStartX(groupX, groupWidth, connectionNodes.length);
		nodes.push(...connectionRow(connectionNodes, connBaseX, connY, dataFor));
	} else {
		nodes.push(...connectionRow(connectionNodes, 0, 0, dataFor));
	}

	const you = buildYouNode(connectionNodes, userConnectionId, connBaseX, connY);
	if (you) nodes.push(you);

	return { nodes, edges: buildEdges(graph, userConnectionId) };
}
