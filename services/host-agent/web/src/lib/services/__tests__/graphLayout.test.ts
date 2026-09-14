// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { describe, expect, it } from 'vitest';
import { detectUserConnection, layoutGraph, NODE_WIDTH, USER_NODE_SIZE } from '../graphLayout';
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

describe('layoutGraph — apps present', () => {
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

describe('layoutGraph — connections only (no apps)', () => {
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
