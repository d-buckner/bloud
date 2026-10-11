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
	import {
		fetchDeveloperGraph,
		type DeveloperGraph
	} from '$lib/clients/developerClient';
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
</style>
