// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Non-disruptive refresh for the polled developer graph.
 *
 * The page re-reads the live graph every few hundred milliseconds. Handing xyflow
 * a brand-new `Node` object for every node on every poll is what makes the graph
 * look like the whole page reloaded. `adoptUserNodes` keeps an internal node only
 * when the incoming user node is the *same reference* it saw last time (its
 * `checkEquality` option), so a fresh object per poll throws away every measured
 * dimension and handle bound and re-measures the entire canvas, and the refit
 * that follows drags the viewport back out from under the operator.
 *
 * `syncGraph` keeps that identity wherever nothing changed. A node whose geometry
 * (type, parent, position, size) matches the one already on screen is kept as the
 * same object, and a status-only change is written onto it rather than replacing
 * it. Only a node that genuinely moved, resized, appeared, or disappeared comes
 * back as a new object, and that is exactly the condition worth re-framing the
 * viewport for.
 */

import type { Edge, Node, XYPosition } from '@xyflow/svelte';

/** One rendering of the graph, as handed to xyflow. */
export interface GraphSnapshot {
	nodes: Node[];
	edges: Edge[];
}

/** A snapshot plus what the caller should do about it. */
export interface GraphSync extends GraphSnapshot {
	/** Nothing on screen changed: skip the assignment entirely. */
	changed: boolean;
	/** A node moved, resized, appeared, or vanished: worth re-framing the view. */
	structureChanged: boolean;
}

const EMPTY: GraphSnapshot = { nodes: [], edges: [] };

function samePosition(a: XYPosition | undefined, b: XYPosition | undefined): boolean {
	if (!a || !b) return a === b;
	return a.x === b.x && a.y === b.y;
}

/**
 * Shallow compare of two node payloads. The payload is a flat record of strings
 * and booleans, so reference equality per key is the whole comparison; a nested
 * object would have to be compared by value to mean anything here.
 */
function sameData(a: unknown, b: unknown): boolean {
	if (a === b) return true;
	if (!a || !b || typeof a !== 'object' || typeof b !== 'object') return false;
	const left = a as Record<string, unknown>;
	const right = b as Record<string, unknown>;
	const keys = Object.keys(left);
	if (keys.length !== Object.keys(right).length) return false;
	return keys.every((k) => left[k] === right[k]);
}

/** Everything about a node that is not its payload: where it is and what it is. */
function samePlacement(node: Node, wanted: Node): boolean {
	return (
		node.type === wanted.type &&
		node.parentId === wanted.parentId &&
		node.style === wanted.style &&
		samePosition(node.position, wanted.position)
	);
}

/** Everything about an edge that is not its animation. */
function sameEndpoints(edge: Edge, wanted: Edge): boolean {
	return edge.source === wanted.source && edge.target === wanted.target && edge.label === wanted.label;
}

function indexById<T extends { id: string }>(items: T[]): Map<string, T> {
	return new Map(items.map((item) => [item.id, item]));
}

/**
 * Fold a freshly laid-out graph into the one already on screen.
 *
 * Nodes and edges that are still where they were come back as the objects the
 * canvas already has, with any new payload written onto them. `structureChanged`
 * answers the only question the viewport cares about: did the shape of the graph
 * change, as opposed to a status dot changing color.
 */
export function syncGraph(prev: GraphSnapshot | null | undefined, next: GraphSnapshot): GraphSync {
	const before = prev ?? EMPTY;
	const prevNodes = indexById(before.nodes);
	const prevEdges = indexById(before.edges);
	let changed = false;
	let structureChanged = false;

	const nodes = next.nodes.map((wanted) => {
		const current = prevNodes.get(wanted.id);
		if (!current || !samePlacement(current, wanted)) {
			changed = true;
			structureChanged = true;
			return wanted;
		}
		if (!sameData(current.data, wanted.data)) {
			changed = true;
			// Written onto the object the canvas is holding, because that object
			// *is* the identity xyflow keys on. Replacing it would drop the
			// measured size and the handle bounds that go with it.
			current.data = wanted.data;
		}
		return current;
	});

	const edges = next.edges.map((wanted) => {
		const current = prevEdges.get(wanted.id);
		if (!current || !sameEndpoints(current, wanted)) {
			changed = true;
			structureChanged = true;
			return wanted;
		}
		if (current.animated !== wanted.animated) {
			changed = true;
			current.animated = wanted.animated;
		}
		return current;
	});

	// Something vanishing changes the shape of the graph even when everything
	// that survived is byte-identical.
	if (nodes.length !== before.nodes.length || edges.length !== before.edges.length) {
		changed = true;
		structureChanged = true;
	}

	if (!changed) {
		return { nodes: before.nodes, edges: before.edges, changed: false, structureChanged: false };
	}
	return { nodes, edges, changed: true, structureChanged };
}
