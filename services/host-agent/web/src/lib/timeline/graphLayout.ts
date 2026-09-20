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
 * the specific connection the operator is using; app boxes inside a bounding
 * group, positioned by dagre. Every container an app declares is laid out
 * inside its app's box (its own dagre pass, so within-app `dependsOn` edges
 * read top-down); apps without containers stay flat leaf nodes. When there are
 * no apps the connection row sits at the origin. Node/edge animation reflects
 * whether endpoints are 'running'/'active'.
 */

import dagre from '@dagrejs/dagre';
import type { Edge, Node } from '@xyflow/svelte';
import type { DeveloperGraph, GraphEdge, GraphNode } from '$lib/clients/developerClient';

export const NODE_WIDTH = 170;
export const NODE_HEIGHT = 60;
export const USER_NODE_SIZE = 64;
/** Container node size. */
export const CONTAINER_WIDTH = 150;
export const CONTAINER_HEIGHT = 38;
/** Inset of a box's contents from its border, and its title row height. */
export const BOX_PADDING = 10;
export const BOX_HEADER = 26;
const GROUP_PADDING = 40;
const CONNECTION_GAP = 100;
const USER_GAP = 60;
/** Horizontal gap between connection nodes in the row. */
const CONN_HGAP = 60;
/** Gaps between the container nodes of one box (dagre nodesep / ranksep). */
const CONTAINER_NODESEP = 24;
const CONTAINER_RANKSEP = 16;

const YOU_ID = '__you__';
const APPS_GROUP_ID = '__apps_group';

interface BoxLayout {
	width: number;
	height: number;
	/** Box-relative top-left position of each container node. */
	positions: Map<string, { x: number; y: number }>;
}

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

/** Dagre layout of one app's containers: box size + box-relative positions. */
function layoutBox(containers: GraphNode[], edges: GraphEdge[]): BoxLayout {
	const g = new dagre.graphlib.Graph();
	g.setGraph({ rankdir: 'TB', nodesep: CONTAINER_NODESEP, ranksep: CONTAINER_RANKSEP });
	g.setDefaultEdgeLabel(() => ({}));
	for (const c of containers) g.setNode(c.id, { width: CONTAINER_WIDTH, height: CONTAINER_HEIGHT });
	for (const e of edges) {
		if (g.hasNode(e.source) && g.hasNode(e.target)) g.setEdge(e.source, e.target);
	}
	dagre.layout(g);

	let minX = Infinity;
	let minY = Infinity;
	let maxX = -Infinity;
	let maxY = -Infinity;
	for (const c of containers) {
		const pos = g.node(c.id);
		minX = Math.min(minX, pos.x - CONTAINER_WIDTH / 2);
		minY = Math.min(minY, pos.y - CONTAINER_HEIGHT / 2);
		maxX = Math.max(maxX, pos.x + CONTAINER_WIDTH / 2);
		maxY = Math.max(maxY, pos.y + CONTAINER_HEIGHT / 2);
	}

	const positions = new Map<string, { x: number; y: number }>();
	for (const c of containers) {
		const pos = g.node(c.id);
		positions.set(c.id, {
			x: pos.x - CONTAINER_WIDTH / 2 - minX + BOX_PADDING,
			y: pos.y - CONTAINER_HEIGHT / 2 - minY + BOX_PADDING + BOX_HEADER
		});
	}

	return {
		width: Math.max(NODE_WIDTH, maxX - minX + BOX_PADDING * 2),
		height: maxY - minY + BOX_PADDING * 2 + BOX_HEADER,
		positions
	};
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

/** Containers grouped by owning app; containers with an unknown app come back loose. */
function groupContainers(nodes: GraphNode[], appNodeIds: Set<string>) {
	const byApp = new Map<string, GraphNode[]>();
	const loose: GraphNode[] = [];
	for (const n of nodes) {
		if (n.nodeType !== 'container') continue;
		if (!n.parentId || !appNodeIds.has(n.parentId)) {
			loose.push(n);
			continue;
		}
		const siblings = byApp.get(n.parentId);
		if (siblings) siblings.push(n);
		else byApp.set(n.parentId, [n]);
	}
	return { byApp, loose };
}

/** How much room every top-level node needs: app boxes grow to fit their containers. */
function measureTopLevel(
	appNodes: GraphNode[],
	byApp: Map<string, GraphNode[]>,
	loose: GraphNode[],
	edges: GraphEdge[]
) {
	const boxes = new Map<string, BoxLayout>();
	const sizes = new Map<string, { width: number; height: number }>();
	for (const n of appNodes) {
		const containers = byApp.get(n.id);
		if (!containers) {
			sizes.set(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT });
			continue;
		}
		const box = layoutBox(containers, edges);
		boxes.set(n.id, box);
		sizes.set(n.id, { width: box.width, height: box.height });
	}
	for (const n of loose) sizes.set(n.id, { width: CONTAINER_WIDTH, height: CONTAINER_HEIGHT });
	return { boxes, sizes };
}

/** Dagre over the top-level nodes → their centers, plus the axis-aligned bounds. */
function layoutTopLevel(
	ids: string[],
	sizes: Map<string, { width: number; height: number }>,
	edges: GraphEdge[]
) {
	const idSet = new Set(ids);
	const g = new dagre.graphlib.Graph();
	g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 });
	g.setDefaultEdgeLabel(() => ({}));
	for (const id of ids) g.setNode(id, sizes.get(id)!);
	for (const e of edges) {
		if (idSet.has(e.source) && idSet.has(e.target)) g.setEdge(e.source, e.target);
	}
	dagre.layout(g);

	let minX = Infinity;
	let minY = Infinity;
	let maxX = -Infinity;
	let maxY = -Infinity;
	for (const id of ids) {
		const pos = g.node(id);
		const size = sizes.get(id)!;
		minX = Math.min(minX, pos.x - size.width / 2);
		minY = Math.min(minY, pos.y - size.height / 2);
		maxX = Math.max(maxX, pos.x + size.width / 2);
		maxY = Math.max(maxY, pos.y + size.height / 2);
	}
	return { g, bounds: { x: minX, y: minY, width: maxX - minX, height: maxY - minY } };
}

interface LayoutContext {
	boxes: Map<string, BoxLayout>;
	byApp: Map<string, GraphNode[]>;
	/** Group-relative top-left for containers whose app is not in the graph. */
	loosePositions: Map<string, { x: number; y: number }>;
	dataFor: (n: GraphNode) => Record<string, unknown>;
}

/** Every container node, positioned inside the box it belongs to (or the group). */
function containerNodes(appNodes: GraphNode[], loose: GraphNode[], ctx: LayoutContext): Node[] {
	const placed: Array<{ node: GraphNode; position: { x: number; y: number }; parentId: string }> = [];
	for (const app of appNodes) {
		const box = ctx.boxes.get(app.id);
		if (!box) continue;
		for (const c of ctx.byApp.get(app.id) ?? []) {
			placed.push({ node: c, position: box.positions.get(c.id)!, parentId: app.id });
		}
	}
	for (const c of loose) {
		placed.push({ node: c, position: ctx.loosePositions.get(c.id)!, parentId: APPS_GROUP_ID });
	}
	return placed.map(({ node, position, parentId }) => ({
		id: node.id,
		type: 'container',
		position,
		parentId,
		style: `width: ${CONTAINER_WIDTH}px; height: ${CONTAINER_HEIGHT}px;`,
		data: ctx.dataFor(node)
	}));
}

/** Group-relative top-left for containers whose owning app is not in the graph. */
function looseContainerPositions(
	loose: GraphNode[],
	placement: { node(id: string): { x: number; y: number } },
	group: { x: number; y: number }
): Map<string, { x: number; y: number }> {
	const positions = new Map<string, { x: number; y: number }>();
	for (const c of loose) {
		const pos = placement.node(c.id);
		positions.set(c.id, {
			x: pos.x - CONTAINER_WIDTH / 2 - group.x,
			y: pos.y - CONTAINER_HEIGHT / 2 - group.y
		});
	}
	return positions;
}

/** Lay the whole developer graph out to xyflow nodes + edges. */
export function layoutGraph(graph: DeveloperGraph, hostname: string): { nodes: Node[]; edges: Edge[] } {
	const appNodes = graph.nodes.filter((n) => n.nodeType === 'app');
	const connectionNodes = graph.nodes.filter((n) => n.nodeType === 'connection');
	const appNodeIds = new Set(appNodes.map((n) => n.id));
	const { byApp, loose } = groupContainers(graph.nodes, appNodeIds);
	const { boxes, sizes } = measureTopLevel(appNodes, byApp, loose, graph.edges);
	const userConnectionId = detectUserConnection(graph, hostname);

	const sources = new Set(graph.edges.map((e) => e.source));
	const targets = new Set(graph.edges.map((e) => e.target));
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

	const topLevelIds = [...appNodeIds, ...loose.map((n) => n.id)];
	if (topLevelIds.length === 0) {
		const nodes = connectionRow(connectionNodes, 0, 0, dataFor);
		const you = buildYouNode(connectionNodes, userConnectionId, 0, 0);
		if (you) nodes.push(you);
		return { nodes, edges: buildEdges(graph, userConnectionId) };
	}
	const { g, bounds } = layoutTopLevel(topLevelIds, sizes, graph.edges);
	const group = { x: bounds.x - GROUP_PADDING, y: bounds.y - GROUP_PADDING };
	const groupWidth = bounds.width + GROUP_PADDING * 2;
	const groupHeight = bounds.height + GROUP_PADDING * 2;

	const nodes: Node[] = [
		{
			id: APPS_GROUP_ID,
			type: 'group',
			position: group,
			style: `width: ${groupWidth}px; height: ${groupHeight}px;`,
			data: {}
		}
	];
	// Parents before children: app nodes first, then the containers they hold.
	for (const n of appNodes) {
		const size = sizes.get(n.id)!;
		const pos = g.node(n.id);
		const position = { x: pos.x - size.width / 2 - group.x, y: pos.y - size.height / 2 - group.y };
		if (!boxes.has(n.id)) {
			nodes.push({ id: n.id, type: 'app', position, parentId: APPS_GROUP_ID, data: dataFor(n) });
			continue;
		}
		nodes.push({
			id: n.id,
			type: 'appBox',
			position,
			parentId: APPS_GROUP_ID,
			style: `width: ${size.width}px; height: ${size.height}px;`,
			data: dataFor(n)
		});
	}
	nodes.push(
		...containerNodes(appNodes, loose, {
			boxes,
			byApp,
			loosePositions: looseContainerPositions(loose, g, group),
			dataFor
		})
	);

	const connY = group.y - NODE_HEIGHT - CONNECTION_GAP;
	const connBaseX = connectionStartX(group.x, groupWidth, connectionNodes.length);
	nodes.push(...connectionRow(connectionNodes, connBaseX, connY, dataFor));

	const you = buildYouNode(connectionNodes, userConnectionId, connBaseX, connY);
	if (you) nodes.push(you);

	return { nodes, edges: buildEdges(graph, userConnectionId) };
}
