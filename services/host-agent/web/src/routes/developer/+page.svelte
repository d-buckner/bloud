<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import {
		Background,
		Controls,
		SvelteFlow,
		type Edge,
		type Node,
		type NodeTypes
	} from '@xyflow/svelte';
	import { layoutGraph } from '$lib/timeline/graphLayout';
	import { syncGraph } from '$lib/timeline/graphSync';
	import { fetchDeveloperGraph, type DeveloperGraph, type GraphNode } from '$lib/clients/developerClient';
	import AppNode from '$lib/graph/AppNode.svelte';
	import AppBox from '$lib/graph/AppBox.svelte';
	import AppGroup from '$lib/graph/AppGroup.svelte';
	import ContainerNode from '$lib/graph/ContainerNode.svelte';
	import { phaseWord, showsPhase } from '$lib/graph/phase';

	import '@xyflow/svelte/dist/style.css';

	/** Shared by the initial fit and the Controls fit button so the two agree. */
	const FIT_OPTIONS = { padding: 0.08, duration: 250 };

	const nodeTypes: NodeTypes = {
		app: AppNode as any,
		appBox: AppBox as any,
		appGroup: AppGroup as any,
		container: ContainerNode as any
	};

	/** The canvas is mounted before the first payload and filled in after. */
	let loaded = $state(false);
	let error = $state('');
	let nodes = $state<Node[]>([]);
	let edges = $state<Edge[]>([]);
	/**
	 * The payload the canvas was built from, kept alongside the xyflow nodes
	 * because the group frame reads the engine's state, which does not survive
	 * the trip through the layout as a thing of its own.
	 */
	let payload = $state<DeveloperGraph | null>(null);

	function extractErrorMessage(err: unknown): string {
		if (err && typeof err === 'object' && 'message' in err) {
			return (err as { message: string }).message;
		}
		return 'Failed to load developer graph';
	}

	let inflight = false;

	async function load() {
		if (inflight) return;
		inflight = true;
		try {
			const graph = await fetchDeveloperGraph();
			const next = layoutGraph(graph);
			payload = graph;
			// Keep the node objects the canvas already has wherever nothing changed.
			// xyflow drops a node's measured size and handle bounds when it is handed
			// a new object, so replacing all of them on every poll re-measures the
			// whole canvas.
			const sync = syncGraph({ nodes, edges }, next);
			if (sync.changed) {
				nodes = sync.nodes;
				edges = sync.edges;
			}
			error = '';
			loaded = true;
		} catch (err) {
			error = extractErrorMessage(err);
		} finally {
			inflight = false;
		}
	}

	let interval: ReturnType<typeof setInterval> | undefined;

	/**
	 * The same reading the canvas draws, as a table.
	 *
	 * A canvas drawn by a layout library is not operable by a keyboard: the nodes
	 * are positioned divs, they are not focusable, and the only thing a Tab key
	 * reaches is the four zoom buttons. Rather than rebuild the diagram as a focus
	 * ring walking a graph, the page offers the same facts in the form the platform
	 * already knows how to traverse. Screen readers get headings and rows; the
	 * keyboard gets a disclosure and a scroll region.
	 */
	let nodeRows = $derived.by(() => {
		if (!payload) return [];
		return payload.nodes.map((n) => {
			const phase = phaseWord(n.phase, n.status);
			return {
				id: n.id,
				name: n.displayName,
				kind: kindWord(n),
				state: showsPhase(phase) ? phase : n.status,
				reason: n.reason ?? ''
			};
		});
	});

	function kindWord(n: GraphNode): string {
		if (n.nodeType === 'connection') return 'public address';
		if (n.nodeType === 'container') return 'container';
		if (n.nodeType === 'service') return 'instance service';
		return n.isSystem ? 'system app' : 'app';
	}

	onMount(() => {
		void load();
		interval = setInterval(() => {
			if (document.hidden) return;
			void load();
		}, 500);
		return () => {
			if (interval) clearInterval(interval);
		};
	});
</script>

<svelte:head>
	<title>Developer · Bloud</title>
</svelte:head>

<div class="graph-container">
	<!-- The diagram is the whole page, so there is no title of its own to sit next
	     to. A page still has to have one: it is how a screen reader names where you
	     are when the route changes, and it is the first thing anyone navigating by
	     heading lands on. -->
	<h1 class="visually-hidden">System architecture</h1>

	<SvelteFlow
		{nodes}
		{edges}
		{nodeTypes}
		colorMode="light"
		fitView
		fitViewOptions={FIT_OPTIONS}
		nodesDraggable={false}
		nodesConnectable={false}
		elementsSelectable={false}
		panOnDrag
		zoomOnScroll
		zoomOnPinch
		zoomOnDoubleClick
		preventScrolling
		minZoom={0.2}
		maxZoom={2}
	>
		<Background />
		<Controls position="bottom-right" showLock={false} fitViewOptions={FIT_OPTIONS} />
	</SvelteFlow>

	<!-- Legend and text outline share one pinned column, so they stack instead of
	     landing on top of each other. -->
	<div class="overlay">
		<!-- The state is a colour and a 7px dot, which is the least durable way to
		     say anything about a system that is supposed to be self-healing. The
		     legend is what makes the colour mean something to a first reader. -->
		<div class="legend" role="group" aria-label="What the diagram colours mean">
			<span class="legend-item"><span class="dot running"></span> running</span>
			<span class="legend-item"><span class="dot failed"></span> stopped or failed</span>
			<span class="legend-item"><span class="dot working"></span> a pass is moving through</span>
			<span class="legend-item"><span class="dot unprobed"></span> not reconciled (address, catalog)</span>
			<span class="legend-item"><span class="frame system"></span> system app</span>
		</div>

		<details class="outline">
			<summary>Text outline of the graph</summary>
			<div class="outline-body">
				<table>
					<thead>
						<tr>
							<th scope="col">Node</th>
							<th scope="col">Kind</th>
							<th scope="col">State</th>
							<th scope="col">Why it stopped</th>
						</tr>
					</thead>
					<tbody>
						{#each nodeRows as row (row.id)}
							<tr>
								<th scope="row">{row.name}</th>
								<td>{row.kind}</td>
								<td>{row.state}</td>
								<td>{row.reason}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</details>
	</div>

	{#if !loaded && !error}
		<div class="canvas-note">Loading graph...</div>
	{:else if loaded && nodes.length === 0}
		<div class="canvas-note">No apps installed.</div>
	{/if}

	{#if error}
		<!-- A poll that fails while a good graph is on screen is a note at the top,
		     not a teardown: the last layout stays put and the next poll replaces it. -->
		<div class="canvas-error">{error}</div>
	{/if}
</div>

<style>
	.graph-container {
		height: 100vh;
		position: relative;
		overflow: hidden;
	}

	.graph-container :global(.svelte-flow__attribution) {
		display: none;
	}

	/* The zoom controls ship as a stack of white boxes with hairline borders and a
	   drop shadow, which is the card vocabulary: they read as four small objects
	   sitting on the diagram rather than as the diagram's own furniture. Same
	   treatment the panel got, taken further: no border, no shadow, and the glyphs
	   carry the affordance. */
	.graph-container :global(.svelte-flow__controls) {
		border: none;
		border-radius: 6px;
		background: transparent;
		box-shadow: none;
	}

	.graph-container :global(.svelte-flow__controls-button) {
		border: none;
		border-radius: 6px;
		background: color-mix(in srgb, var(--color-bg-elevated, #fff) 88%, transparent);
		color: var(--color-text-secondary, #57534e);
		fill: currentColor;
	}

	.graph-container :global(.svelte-flow__controls-button:hover) {
		background: var(--color-bg-elevated, #fff);
		color: var(--color-text, #1c1917);
	}

	/* Overlays, not replacements: the canvas and its controls stay mounted underneath,
	   so a poll never tears the view down. */
	.canvas-note {
		position: absolute;
		inset: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--color-text-muted);
		font-size: 0.875rem;
		pointer-events: none;
	}

	.canvas-error {
		position: absolute;
		top: var(--space-md, 16px);
		left: 50%;
		transform: translateX(-50%);
		max-width: min(480px, calc(100% - 32px));
		padding: var(--space-sm, 8px) var(--space-md, 16px);
		font-size: 0.875rem;
		text-align: center;
		color: var(--color-error);
		background: rgba(220, 38, 38, 0.06);
		border: 1px solid rgba(220, 38, 38, 0.18);
		border-radius: var(--radius-md, 8px);
		pointer-events: none;
	}

	.overlay {
		position: absolute;
		top: var(--space-md);
		left: var(--space-md);
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		max-width: min(300px, calc(100% - 32px));
		max-height: calc(100vh - 32px);
	}

	/* Legend and text outline share one furniture: a small panel pinned top-left,
	   the same no-border, no-shadow treatment the zoom controls were given, so
	   neither reads as a card floating on the diagram. */
	.legend,
	.outline {
		font-family: var(--font-sans);
		font-size: 0.75rem;
		background: color-mix(in srgb, var(--color-bg-elevated, #fff) 90%, transparent);
		border-radius: 6px;
	}

	.legend {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: var(--space-sm) var(--space-md);
	}

	.legend-item {
		display: flex;
		align-items: center;
		gap: 6px;
		color: var(--color-text-secondary);
	}

	.dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.dot.running {
		background: var(--color-success);
	}

	.dot.failed {
		background: var(--color-error);
	}

	.dot.working {
		background: var(--color-info);
	}

	/* Hollow, the way the node card draws it: filled means the engine has a state
	   for this node, hollow means the card is only repeating the catalog. */
	.dot.unprobed {
		box-sizing: border-box;
		border: 1.5px solid var(--color-text-secondary);
	}

	.frame {
		width: 10px;
		height: 10px;
		border: 1px solid var(--color-text-secondary);
		border-radius: 3px;
		flex-shrink: 0;
	}

	.frame.system {
		border-style: dashed;
	}

	.outline summary {
		padding: var(--space-sm) var(--space-md);
		cursor: pointer;
		color: var(--color-text-secondary);
	}

	.outline summary:hover {
		color: var(--color-text);
	}

	/* Open, the panel scrolls inside itself. The canvas is 100vh with overflow
	   hidden, so an outline that grew the page would be the clipped content this
	   is meant to replace. */
	.outline[open] {
		overflow: auto;
	}

	.outline-body {
		padding: 0 var(--space-md) var(--space-md);
	}

	.outline table {
		border-collapse: collapse;
		width: 100%;
	}

	.outline th,
	.outline td {
		text-align: left;
		padding: 3px 6px 3px 0;
		vertical-align: top;
		border-bottom: 1px solid var(--color-border-subtle);
	}

	.outline th {
		font-weight: 600;
		color: var(--color-text);
	}

	.outline td {
		color: var(--color-text-secondary);
		font-weight: 400;
	}
</style>
