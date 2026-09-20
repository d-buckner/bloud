// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { describe, expect, it } from 'vitest';
import {
	BOX_HEADER,
	BOX_PADDING,
	CONTAINER_HEIGHT,
	CONTAINER_WIDTH,
	detectUserConnection,
	layoutGraph,
	NODE_WIDTH,
	USER_NODE_SIZE
} from '../graphLayout';
import type { DeveloperGraph, GraphEdge, GraphNode } from '$lib/clients/developerClient';

const node = (id: string, over: Partial<GraphNode> = {}): GraphNode => ({
	id,
	displayName: id,
	status: 'running',
	isSystem: false,
	nodeType: 'app',
	...over
});
const conn = (id: string, over: Partial<GraphNode> = {}): GraphNode =>
	node(id, { nodeType: 'connection', ...over });
const edge = (source: string, target: string): GraphEdge => ({ source, target, label: `${source}->${target}` });

const byId = <T extends { id: string }>(nodes: T[], id: string) => nodes.find((n) => n.id === id);

describe('detectUserConnection', () => {
	const tailnet = conn('conn:tailnet:abc');
	const local = conn('conn:local');
	const g = (nodes: GraphNode[], tailnetDomain?: string): DeveloperGraph => ({
		nodes,
		edges: [],
		tailnetDomain
	});

	it('returns the tailnet connection when the host is under the tailnet domain', () => {
		const graph = g([tailnet, local], 'ts1.ts.net');
		expect(detectUserConnection(graph, 'bloud.ts1.ts.net')).toBe('conn:tailnet:abc');
	});

	it('falls back to LAN when the host is not under the tailnet domain', () => {
		const graph = g([tailnet, local], 'ts1.ts.net');
		expect(detectUserConnection(graph, 'bloud.local')).toBe('conn:local');
	});

	it('falls back to LAN when there is no tailnet connection node', () => {
		const graph = g([local], 'ts1.ts.net');
		expect(detectUserConnection(graph, 'bloud.ts1.ts.net')).toBe('conn:local');
	});

	it('returns null when neither connection exists', () => {
		expect(detectUserConnection(g([node('a')]), 'bloud.local')).toBeNull();
	});
});

describe('layoutGraph: apps present', () => {
	const graph: DeveloperGraph = {
		nodes: [
			node('a'),
			node('b'),
			node('c', { status: 'stopped' }),
			conn('conn:local'),
			conn('conn:tailnet:z')
		],
		edges: [edge('a', 'b'), edge('b', 'c')],
		tailnetDomain: 'ts1.ts.net'
	};
	// Operator reaches through the tailnet connection.
	const { nodes, edges } = layoutGraph(graph, 'bloud.ts1.ts.net');

	it('creates the apps group and parents every app under it', () => {
		expect(byId(nodes, '__apps_group')?.type).toBe('group');
		for (const id of ['a', 'b', 'c']) {
			expect(byId(nodes, id)).toMatchObject({ type: 'app', parentId: '__apps_group' });
		}
	});

	it('places every connection in one horizontal row above the group', () => {
		const conns = [byId(nodes, 'conn:local'), byId(nodes, 'conn:tailnet:z')];
		const ys = conns.map((c) => c!.position.y);
		expect(new Set(ys).size).toBe(1); // all connections share one y
	});

	it('stacks the You node 124px above the connection row', () => {
		const connY = byId(nodes, 'conn:local')!.position.y;
		const you = byId(nodes, '__you__');
		expect(you?.type).toBe('user');
		expect(you!.position.y).toBe(connY - USER_NODE_SIZE - 60);
	});

	it('tracks outgoing/incoming from the app edge set', () => {
		expect(byId(nodes, 'a')!.data).toMatchObject({ hasOutgoing: true, hasIncoming: false });
		expect(byId(nodes, 'b')!.data).toMatchObject({ hasOutgoing: true, hasIncoming: true });
		expect(byId(nodes, 'c')!.data).toMatchObject({ hasOutgoing: false, hasIncoming: true });
	});

	it('animates an edge only when both endpoints are active', () => {
		const ab = edges.find((e) => e.source === 'a' && e.target === 'b');
		const bc = edges.find((e) => e.source === 'b' && e.target === 'c');
		expect(ab?.animated).toBe(true); // running -> running
		expect(bc?.animated).toBe(false); // running -> stopped
	});

	it('adds an animated You→connection edge by the connection status', () => {
		const youEdge = edges.find((e) => e.id === 'e-you');
		expect(youEdge).toMatchObject({ source: '__you__', target: 'conn:tailnet:z', animated: true });
	});
});

describe('layoutGraph: app boxes', () => {
	const graph: DeveloperGraph = {
		nodes: [
			node('immich'),
			node('apps-immich-postgres', { nodeType: 'container', parentId: 'immich' }),
			node('apps-immich-server', { nodeType: 'container', parentId: 'immich' }),
			node('jellyfin'),
			node('apps-jellyfin', { nodeType: 'container', parentId: 'jellyfin' }),
			node('sys:gateway'),
			conn('conn:local')
		],
		edges: [
			edge('apps-immich-server', 'apps-immich-postgres'),
			edge('immich', 'jellyfin')
		]
	};
	const { nodes } = layoutGraph(graph, 'bloud.local');
	const boxSize = (id: string) => {
		const style = byId(nodes, id)!.style as string;
		return {
			width: Number(/width: (\d+)px/.exec(style)?.[1]),
			height: Number(/height: (\d+)px/.exec(style)?.[1])
		};
	};

	it('renders an app with containers as a box under the apps group', () => {
		expect(byId(nodes, 'immich')).toMatchObject({ type: 'appBox', parentId: '__apps_group' });
		expect(byId(nodes, 'jellyfin')).toMatchObject({ type: 'appBox', parentId: '__apps_group' });
	});

	it('parents containers to their app box and lists the parent first', () => {
		for (const id of ['apps-immich-postgres', 'apps-immich-server', 'apps-jellyfin']) {
			const idx = nodes.findIndex((n) => n.id === id);
			expect(nodes[idx]).toMatchObject({ type: 'container' });
			expect(nodes.findIndex((n) => n.id === nodes[idx].parentId!)).toBeLessThan(idx);
		}
	});

	it('keeps every container inside its box bounds', () => {
		const immich = boxSize('immich');
		for (const id of ['apps-immich-postgres', 'apps-immich-server']) {
			const pos = byId(nodes, id)!.position;
			expect(pos.x).toBeGreaterThanOrEqual(BOX_PADDING);
			expect(pos.y).toBeGreaterThanOrEqual(BOX_HEADER);
			expect(pos.x + CONTAINER_WIDTH).toBeLessThanOrEqual(immich.width - BOX_PADDING);
			expect(pos.y + CONTAINER_HEIGHT).toBeLessThanOrEqual(immich.height - BOX_PADDING);
		}
	});

	it('sizes the box to stack its containers', () => {
		expect(boxSize('immich').height).toBeGreaterThan(2 * CONTAINER_HEIGHT);
	});

	it('draws a dependsOn edge below the container that declares it', () => {
		expect(byId(nodes, 'apps-immich-server')!.position.y).toBeLessThan(
			byId(nodes, 'apps-immich-postgres')!.position.y
		);
	});

	it('sizes a single-container box to one container row', () => {
		expect(boxSize('jellyfin').height).toBe(BOX_HEADER + CONTAINER_HEIGHT + BOX_PADDING * 2);
	});

	it('leaves apps without containers as flat nodes', () => {
		expect(byId(nodes, 'sys:gateway')).toMatchObject({ type: 'app', parentId: '__apps_group' });
	});
});

describe('layoutGraph: container whose app is missing', () => {
	const graph: DeveloperGraph = {
		nodes: [
			node('jellyfin'),
			node('apps-jellyfin', { nodeType: 'container', parentId: 'jellyfin' }),
			node('apps-ghost-server', { nodeType: 'container', parentId: 'ghost' })
		],
		edges: []
	};
	const { nodes } = layoutGraph(graph, 'bloud.local');

	it('lays the orphan out under the group instead of dropping it', () => {
		expect(byId(nodes, 'apps-ghost-server')).toMatchObject({
			type: 'container',
			parentId: '__apps_group'
		});
	});
});

describe('layoutGraph: connections only (no apps)', () => {
	const graph: DeveloperGraph = {
		nodes: [conn('conn:local'), conn('conn:tailnet:z')],
		edges: []
	};
	const { nodes, edges } = layoutGraph(graph, 'bloud.local'); // LAN

	it('omits the group and rows connections from the origin', () => {
		expect(nodes.some((n) => n.type === 'group')).toBe(false);
		expect(byId(nodes, 'conn:local')!.position).toEqual({ x: 0, y: 0 });
		expect(byId(nodes, 'conn:tailnet:z')!.position.x).toBe(NODE_WIDTH + 60);
	});

	it('puts the You node above the LAN connection at the origin column', () => {
		const you = byId(nodes, '__you__')!;
		expect(you.type).toBe('user');
		expect(you.position.y).toBe(-(USER_NODE_SIZE + 60)); // connY=0 - SIZE - GAP
		// centered over the LAN connection at x=0
		expect(you.position.x).toBe(NODE_WIDTH / 2 - USER_NODE_SIZE / 2);
	});

	it('animates the You edge by the LAN connection status', () => {
		expect(edges.find((e) => e.id === 'e-you')).toMatchObject({ target: 'conn:local', animated: true });
	});
});
