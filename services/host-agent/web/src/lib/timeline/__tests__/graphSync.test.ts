// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import type { Edge, Node } from '@xyflow/svelte';
import { syncGraph, type GraphSnapshot } from '../graphSync';

const node = (id: string, over: Partial<Node> = {}): Node =>
	({
		id,
		type: 'app',
		position: { x: 0, y: 0 },
		data: { status: 'running' },
		...over
	}) as Node;

const edge = (id: string, source: string, target: string, over: Partial<Edge> = {}): Edge =>
	({ id, source, target, label: `${source}->${target}`, ...over }) as Edge;

const snap = (nodes: Node[], edges: Edge[] = []): GraphSnapshot => ({ nodes, edges });

describe('syncGraph: an empty canvas', () => {
	it('reports an empty baseline as a structure change', () => {
		const res = syncGraph(snap([], []), snap([node('a')]));
		expect(res.changed).toBe(true);
		expect(res.structureChanged).toBe(true);
		expect(res.nodes).toHaveLength(1);
	});

	it('treats a null previous snapshot like an empty one', () => {
		const res = syncGraph(null, snap([node('a')]));
		expect(res.structureChanged).toBe(true);
	});
});

describe('syncGraph: nothing changed', () => {
	it('reports no change at all for an identical snapshot', () => {
		const prev = snap([node('a'), node('b', { position: { x: 10, y: 20 } })], [edge('e0', 'a', 'b')]);
		const next = snap([node('a'), node('b', { position: { x: 10, y: 20 } })], [edge('e0', 'a', 'b')]);
		const res = syncGraph(prev, next);

		expect(res.changed).toBe(false);
		expect(res.structureChanged).toBe(false);
		// The caller assigns this back into state, so an unchanged fold has to come
		// back as the very same arrays or the canvas re-renders for nothing.
		expect(res.nodes).toBe(prev.nodes);
		expect(res.edges).toBe(prev.edges);
	});
});

describe('syncGraph: payload-only changes keep object identity', () => {
	it('keeps the node object when only its payload changed', () => {
		const prev = snap([node('a', { data: { status: 'starting' } })]);
		const next = snap([node('a', { data: { status: 'running' } })]);
		const res = syncGraph(prev, next);

		expect(res.changed).toBe(true);
		expect(res.structureChanged).toBe(false);
		expect(res.nodes[0]).toBe(prev.nodes[0]);
		expect(res.nodes[0].data).toEqual({ status: 'running' });
	});

	it('keeps the edge object when only its animation changed', () => {
		const prev = snap([node('a'), node('b')], [edge('e0', 'a', 'b', { animated: false })]);
		const next = snap([node('a'), node('b')], [edge('e0', 'a', 'b', { animated: true })]);
		const res = syncGraph(prev, next);

		expect(res.changed).toBe(true);
		expect(res.structureChanged).toBe(false);
		expect(res.edges[0]).toBe(prev.edges[0]);
		expect(res.edges[0].animated).toBe(true);
	});

	it('treats a payload key set change as a change', () => {
		const prev = snap([node('a', { data: { status: 'running' } })]);
		const next = snap([node('a', { data: { status: 'running', extra: true } })]);
		const res = syncGraph(prev, next);

		expect(res.changed).toBe(true);
		expect(res.structureChanged).toBe(false);
		expect(res.nodes[0].data).toEqual({ status: 'running', extra: true });
	});
});

describe('syncGraph: structural changes', () => {
	it('replaces the node object when the node moved', () => {
		const prev = snap([node('a', { position: { x: 0, y: 0 } })]);
		const next = snap([node('a', { position: { x: 0, y: 120 } })]);
		const res = syncGraph(prev, next);

		expect(res.structureChanged).toBe(true);
		expect(res.nodes[0]).toBe(next.nodes[0]);
	});

	it('does not mistake a zero position for a missing one', () => {
		const prev = snap([node('a', { position: { x: 0, y: 0 } })]);
		const next = snap([node('a', { position: { x: 0, y: 1 } })]);
		expect(syncGraph(prev, next).structureChanged).toBe(true);
	});

	it('counts a box resize as a structure change', () => {
		const prev = snap([node('a', { type: 'appBox', style: 'width: 100px; height: 80px;' })]);
		const next = snap([node('a', { type: 'appBox', style: 'width: 100px; height: 120px;' })]);
		expect(syncGraph(prev, next).structureChanged).toBe(true);
	});

	it('counts a node type change as a structure change', () => {
		const prev = snap([node('a', { type: 'app' })]);
		const next = snap([node('a', { type: 'appBox', style: 'width: 200px; height: 80px;' })]);
		expect(syncGraph(prev, next).structureChanged).toBe(true);
	});

	it('counts a reparent as a structure change', () => {
		const prev = snap([node('c', { type: 'container' })]);
		const next = snap([node('c', { type: 'container', parentId: 'app:x' })]);
		expect(syncGraph(prev, next).structureChanged).toBe(true);
	});

	it('reports a removed node as a structure change', () => {
		const prev = snap([node('a'), node('b')]);
		const res = syncGraph(prev, snap([node('a')]));

		expect(res.changed).toBe(true);
		expect(res.structureChanged).toBe(true);
		expect(res.nodes).toHaveLength(1);
	});

	it('reports a removed edge as a structure change', () => {
		const prev = snap([node('a'), node('b')], [edge('e0', 'a', 'b')]);
		const res = syncGraph(prev, snap([node('a'), node('b')], []));

		expect(res.changed).toBe(true);
		expect(res.structureChanged).toBe(true);
	});
});

describe('syncGraph: a mixed update', () => {
	it('folds one moved node, one status change, and one new node', () => {
		const moved = node('moved', { position: { x: 0, y: 0 } });
		const steady = node('steady', { data: { status: 'starting' } });
		const prev = snap([moved, steady]);
		const next = snap([
			node('moved', { position: { x: 0, y: 300 } }),
			node('steady', { data: { status: 'running' } }),
			node('fresh')
		]);

		const res = syncGraph(prev, next);

		expect(res.structureChanged).toBe(true);
		expect(res.nodes[0]).toBe(next.nodes[0]);
		expect(res.nodes[1]).toBe(prev.nodes[1]);
		expect(res.nodes[1].data).toEqual({ status: 'running' });
		expect(res.nodes[2].id).toBe('fresh');
	});
});
