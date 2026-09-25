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
				fitView
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

		<footer class="legend">
			<div class="legend-item">
				<span class="key box"></span>
				<span>one box per app, one node inside it per container the app declares</span>
			</div>
			<div class="legend-item">
				<span class="key system"></span>
				<span>dashed: system app, the infrastructure Bloud runs underneath every other app</span>
			</div>
			<div class="legend-item">
				<span class="key edge"></span>
				<span>
					edges are integrations: <code>proxy</code> from the proxy to what it routes,
					<code>ldap</code> / <code>forward-auth</code> / <code>native-oidc</code> to the
					identity provider
				</span>
			</div>
			<div class="legend-item">
				<span class="key status"></span>
				<span>catalog entries, not live status: nothing here is running</span>
			</div>
		</footer>
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

	/* The legend is part of the picture, not an overlay on it: the renderer
	   crops to the graph, and this strip is what makes the crop readable
	   without the README having to explain the notation itself. */
	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 8px 28px;
		padding: 12px 20px;
		border-top: 1px solid var(--color-border);
		background: var(--color-bg-elevated);
	}

	.legend-item {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		max-width: 420px;
		font-size: 0.6875rem;
		line-height: 1.4;
		color: var(--color-text-secondary);
	}

	.legend code {
		font-family: var(--font-mono);
		font-size: 0.625rem;
		color: var(--color-text);
	}

	.key {
		flex-shrink: 0;
		width: 14px;
		height: 11px;
		margin-top: 2px;
		border: 1px solid var(--color-border-strong);
		border-radius: 3px;
		background: var(--color-bg-elevated);
	}

	.key.system {
		border-style: dashed;
		background: rgba(120, 113, 108, 0.06);
	}

	.key.edge {
		height: 0;
		margin-top: 7px;
		border: 0;
		border-top: 1.5px solid var(--color-border-strong);
	}

	.key.status {
		width: 8px;
		height: 8px;
		margin-top: 4px;
		border: 0;
		border-radius: 50%;
		background: #9ca3af;
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
