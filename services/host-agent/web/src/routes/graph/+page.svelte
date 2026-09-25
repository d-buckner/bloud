<!--
	The catalog graph, drawn by the dashboard's own graph components.

	`bloud depgraph --json` derives the node and edge set from every app's
	metadata.yaml, and this page renders it with the same layout and node
	components the live developer graph uses. The difference is the input: the
	live graph draws the apps one instance has installed, this one draws the
	whole catalog, with nothing running behind it.

	`scripts/render-graph.mjs` opens this page in a headless browser and
	screenshots it into the image the README embeds, which is why the page
	exists at all: the picture is the product, and it is generated from the
	same code that draws the real thing so the two cannot drift.
-->
<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount, tick } from 'svelte';
	import { SvelteFlow, type Edge, type Node, type NodeTypes } from '@xyflow/svelte';
	import type { DeveloperGraph } from '$lib/clients/developerClient';
	import AppBox from '$lib/graph/AppBox.svelte';
	import AppNode from '$lib/graph/AppNode.svelte';
	import ContainerNode from '$lib/graph/ContainerNode.svelte';
	import { layoutGraph } from '$lib/timeline/graphLayout';
	import FlowBridge from './FlowBridge.svelte';

	import '@xyflow/svelte/dist/style.css';

	const nodeTypes: NodeTypes = {
		app: AppNode as any,
		appBox: AppBox as any,
		container: ContainerNode as any
	};

	/**
	 * How the flow opens. The render harness asks for no fit and an explicit
	 * zoom because Chromium rasterizes a transformed layer once and then scales
	 * that bitmap: fit first, force zoom later, and the picture comes out of a
	 * resampled raster instead of fresh type. Starting at the zoom that gets
	 * captured is what keeps the text crisp.
	 */
	const opening = $derived.by(() => {
		const params = new URLSearchParams(window.location.search);
		const zoom = Number(params.get('zoom'));
		return {
			fit: params.get('fit') !== '0',
			zoom: Number.isFinite(zoom) && zoom > 0 ? zoom : 1
		};
	});

	let nodes = $state<Node[]>([]);
	let edges = $state<Edge[]>([]);
	let error = $state('');
	let ready = $state(false);

	/**
	 * The catalog snapshot comes from the render harness when it injected one
	 * (so the derived JSON never has to be served from the built tree), and
	 * from the static path otherwise, which is what a snapshot dropped in
	 * `static/` uses when the page is opened by hand.
	 */
	async function loadCatalogGraph(): Promise<DeveloperGraph> {
		const injected = window.__BLOUD_CATALOG_GRAPH__;
		if (injected) return injected;
		const res = await fetch('/catalog-graph.json', { headers: { accept: 'application/json' } });
		if (!res.ok) {
			throw new Error(`catalog graph not available (HTTP ${res.status})`);
		}
		return (await res.json()) as DeveloperGraph;
	}

	/**
	 * The renderer waits on this flag rather than on a timeout: the picture is
	 * only correct once the nodes are laid out and the fonts the dashboard
	 * uses are the fonts that were measured.
	 */
	async function markReady() {
		await tick();
		if (document.fonts?.ready) {
			await document.fonts.ready;
		}
		ready = true;
		document.body.dataset.graphReady = 'true';
	}

	onMount(() => {
		loadCatalogGraph()
			.then((graph) => {
				const layout = layoutGraph(graph, window.location.hostname);
				nodes = layout.nodes;
				edges = layout.edges;
			})
			.catch((err: unknown) => {
				error = err instanceof Error ? err.message : 'failed to load the catalog graph';
				document.body.dataset.graphError = error;
			})
			.then(markReady);
	});
</script>

<svelte:head>
	<title>Bloud catalog graph</title>
</svelte:head>

<div class="canvas">
	{#if error}
		<p class="notice error">{error}</p>
	{:else if nodes.length > 0}
		<div class="graph">
			<SvelteFlow
				{nodes}
				{edges}
				{nodeTypes}
				fitView={opening.fit}
				viewport={{ x: 0, y: 0, zoom: opening.zoom }}
				fitViewOptions={{ padding: 0.06, minZoom: 0.05, duration: 0 }}
				colorMode="light"
				nodesDraggable={false}
				nodesConnectable={false}
				elementsSelectable={false}
				panOnDrag={false}
				zoomOnScroll={false}
				zoomOnPinch={false}
				zoomOnDoubleClick={false}
				preventScrolling={false}
			>
				<FlowBridge />
			</SvelteFlow>
		</div>
	{:else if !ready}
		<p class="notice">Laying out the catalog graph...</p>
	{:else}
		<p class="notice">The catalog is empty.</p>
	{/if}
</div>

<style>
	.canvas {
		display: flex;
		flex-direction: column;
		width: 100%;
		height: 100vh;
		background: var(--color-bg);
	}

	.graph {
		position: relative;
		flex: 1;
		min-height: 0;
	}

	.graph :global(.svelte-flow__attribution) {
		display: none;
	}

	.notice {
		padding: var(--space-xl);
		font-size: 0.875rem;
		color: var(--color-text-muted);
		text-align: center;
	}

	.notice.error {
		color: var(--color-error);
	}
</style>
