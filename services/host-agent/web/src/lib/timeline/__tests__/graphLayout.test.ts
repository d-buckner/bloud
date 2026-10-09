// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import {
	BOX_HEADER,
	BOX_PADDING,
	CONTAINER_HEIGHT,
	CONTAINER_WIDTH,
	layoutGraph,
	NODE_WIDTH
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

/** Read `width: Npx; height: Npx` off a node's inline style. */
const boxSize = (style: unknown): { width: number; height: number } => {
	const m = /width: (\d+(?:\.\d+)?)px; height: (\d+(?:\.\d+)?)px/.exec(String(style ?? ''));
	return m ? { width: Number(m[1]), height: Number(m[2]) } : { width: 0, height: 0 };
};

describe('layoutGraph: apps present', () => {
	const graph: DeveloperGraph = {
		nodes: [
			node('a'),
			node('b'),
			node('c', { status: 'stopped' }),
			conn('conn:local', { displayName: 'bloud.example.com' })
		],
		edges: [edge('a', 'b'), edge('b', 'c')]
	};
	const { nodes, edges } = layoutGraph(graph);

	it('creates the apps group and parents every app under it', () => {
		expect(byId(nodes, '__apps_group')?.type).toBe('group');
		for (const id of ['a', 'b', 'c']) {
			expect(byId(nodes, id)).toMatchObject({ type: 'app', parentId: '__apps_group' });
		}
	});

	it('places the connection in the row above the group', () => {
		const c = byId(nodes, 'conn:local');
		expect(c).toBeDefined();
		expect(c!.position.y).toBeLessThan(byId(nodes, '__apps_group')!.position.y);
	});

	it('labels the connection with the address the backend sent', () => {
		// One node for the ingress, named with the configured public address.
		// The operator avatar this replaced was a frontend invention: it drew a
		// second node above the connection for whoever was looking, which said
		// nothing the address did not already say.
		expect(byId(nodes, 'conn:local')!.data).toMatchObject({ displayName: 'bloud.example.com' });
		expect(byId(nodes, '__you__')).toBeUndefined();
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

	it('draws no edge into the ingress that the backend did not declare', () => {
		// The graph has no conn:local edge of its own here, so nothing may appear.
		// The synthetic avatar edge this replaces reached the canvas from a
		// layout guess, not from the payload.
		expect(edges.some((e) => e.id === 'e-you')).toBe(false);
		expect(edges.some((e) => e.target === 'conn:local')).toBe(false);
	});
});

describe('layoutGraph: service node edges', () => {
	// The AI Model node is the instance's own provider: never probed, so it
	// carries `external` rather than a lifecycle status, and the backend drops
	// it the moment Settings -> AI has no enabled upstream. Its presence in the
	// payload is the liveness claim, so an edge into it animates.
	const graph: DeveloperGraph = {
		nodes: [
			node('hermes'),
			node('affine', { status: 'stopped' }),
			node('ai:instance', { status: 'external', nodeType: 'service' }),
			conn('conn:local')
		],
		edges: [edge('hermes', 'ai:instance'), edge('affine', 'ai:instance')],
	};
	const { edges } = layoutGraph(graph);

	it('animates a running consumer\'s edge into the service node', () => {
		const e = edges.find((x) => x.source === 'hermes' && x.target === 'ai:instance');
		expect(e?.animated).toBe(true);
	});

	it('still requires the consumer side to be live', () => {
		const e = edges.find((x) => x.source === 'affine' && x.target === 'ai:instance');
		expect(e?.animated).toBe(false);
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
	const { nodes } = layoutGraph(graph);
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
	const { nodes } = layoutGraph(graph);

	it('lays the orphan out under the group instead of dropping it', () => {
		expect(byId(nodes, 'apps-ghost-server')).toMatchObject({
			type: 'container',
			parentId: '__apps_group'
		});
	});
});

describe('layoutGraph: service nodes', () => {
	// A service is a provider the instance supplies itself rather than an app
	// it installs: no box, no containers, consumers point at it with an
	// integration edge. It sits outside the apps group, in a row below it, the
	// mirror of the connection row above.
	const graph: DeveloperGraph = {
		nodes: [
			node('traefik'),
			node('hermes'),
			node('ai:instance', {
				displayName: 'AI Model',
				nodeType: 'service',
				status: 'external',
				isSystem: false
			})
		],
		edges: [edge('hermes', 'ai:instance')]
	};
	const { nodes, edges } = layoutGraph(graph);

	it('lays the service outside the apps group, below it, as a flat node', () => {
		const service = byId(nodes, 'ai:instance')!;
		expect(service.type).toBe('app');
		expect(service.parentId).toBeUndefined();

		const group = byId(nodes, '__apps_group')!;
		const { width, height } = boxSize(group.style);
		expect(service.position.y).toBeGreaterThan(group.position.y + height);
		// centered over the group, the way the connection row is over it from above
		expect(service.position.x + NODE_WIDTH / 2).toBeCloseTo(group.position.x + width / 2, 5);
	});

	it('keeps the service node type on the data so layout can place it', () => {
		expect(byId(nodes, 'ai:instance')!.data).toMatchObject({
			nodeType: 'service',
			displayName: 'AI Model',
			status: 'external'
		});
	});

	it('draws the consumer edge into the service', () => {
		expect(edges.some((e) => e.source === 'hermes' && e.target === 'ai:instance')).toBe(true);
	});

	it('lays a service out even when nothing points at it', () => {
		const lonely = layoutGraph({
			nodes: [node('a'), node('ai:instance', { nodeType: 'service' })],
			edges: []
		});
		expect(byId(lonely.nodes, 'ai:instance')).toMatchObject({ type: 'app' });
		expect(byId(lonely.nodes, 'ai:instance')!.parentId).toBeUndefined();
	});

	it('still lays the service out when there are no apps at all', () => {
		const noApps = layoutGraph({
			nodes: [conn('conn:local'), node('ai:instance', { nodeType: 'service' })],
			edges: []
		});
		expect(byId(noApps.nodes, 'ai:instance')).toBeDefined();
		expect(byId(noApps.nodes, 'ai:instance')!.position.y).toBeGreaterThan(
			byId(noApps.nodes, 'conn:local')!.position.y
		);
	});
});

describe('layoutGraph: connections only (no apps)', () => {
	const graph: DeveloperGraph = {
		nodes: [conn('conn:local', { displayName: 'localhost:8080' })],
		edges: []
	};
	const { nodes, edges } = layoutGraph(graph);

	it('omits the group and rows connections from the origin', () => {
		expect(nodes.some((n) => n.type === 'group')).toBe(false);
		expect(byId(nodes, 'conn:local')!.position).toEqual({ x: 0, y: 0 });
	});

	it('draws the connection alone: no avatar node, no invented edge', () => {
		expect(nodes).toHaveLength(1);
		expect(byId(nodes, 'conn:local')!.data).toMatchObject({ displayName: 'localhost:8080' });
		expect(edges).toHaveLength(0);
	});
});
