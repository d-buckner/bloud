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
	import type { DeveloperGraph } from '$lib/clients/developerClient';
	import { graphView, select, startGraphPoll, stopGraphPoll } from '$lib/store';
	import AppNode from '$lib/graph/AppNode.svelte';
	import AppBox from '$lib/graph/AppBox.svelte';
	import AppGroup from '$lib/graph/AppGroup.svelte';
	import ContainerNode from '$lib/graph/ContainerNode.svelte';

	import '@xyflow/svelte/dist/style.css';

	/** Shared by the initial fit and the Controls fit button so the two agree. */
	const FIT_OPTIONS = { padding: 0.08, duration: 250 };

	const nodeTypes: NodeTypes = {
		app: AppNode as any,
		appBox: AppBox as any,
		appGroup: AppGroup as any,
		container: ContainerNode as any
	};

	// The read is the store's: when it happens, whether one is in flight, what
	// failed. This page only projects what landed onto a canvas, which is the part
	// that genuinely belongs here: `layoutGraph` runs dagre, and dagre is in this
	// route's chunk precisely so that nobody on the home page downloads it.
	const view = select(graphView);

	let loaded = $derived(view.current.loaded);
	let error = $derived(view.current.error);
	const payload = $derived(view.current.graph.payload);

	let nodes = $state<Node[]>([]);
	let edges = $state<Edge[]>([]);

	/** The payload this canvas was last built from, compared by identity: the
	 *  poll hands over a fresh object every 500ms, and re-running dagre on
	 *  identical bytes is the most expensive way to change nothing. */
	let laidOut: DeveloperGraph | null = null;

	$effect(() => {
		const graph = payload;
		if (!graph || graph === laidOut) return;
		laidOut = graph;
		// Keep the node objects the canvas already has wherever nothing changed.
		// xyflow drops a node's measured size and handle bounds when it is handed
		// a new object, so replacing all of them on every poll re-measures the
		// whole canvas.
		const sync = syncGraph({ nodes, edges }, layoutGraph(graph));
		if (sync.changed) {
			nodes = sync.nodes;
			edges = sync.edges;
		}
	});

	onMount(() => {
		startGraphPoll();
		return stopGraphPoll;
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

	{#if !loaded && !error}
		<div class="canvas-note">Loading graph...</div>
	{:else if loaded && nodes.length === 0}
		<div class="canvas-note">No apps installed.</div>
	{/if}

	{#if error}
		<!-- A poll that fails while a good graph is on screen is a note at the top,
		     not a teardown: the last layout stays put and the next poll replaces it. -->
		<div class="canvas-error" role="status">
			{error}{#if view.current.stale} (showing the last good read){/if}
		</div>
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
</style>
