<script lang="ts">
	import { onMount } from 'svelte';
	import { SvelteFlow, type Node, type Edge, type NodeTypes } from '@xyflow/svelte';
	import dagre from '@dagrejs/dagre';
	import { fetchDeveloperGraph, type DeveloperGraph } from '$lib/clients/developerClient';
	import AppNode from './AppNode.svelte';

	import '@xyflow/svelte/dist/style.css';

	const nodeTypes: NodeTypes = {
		app: AppNode as any
	};

	let loading = $state(true);
	let error = $state('');
	let nodes = $state<Node[]>([]);
	let edges = $state<Edge[]>([]);

	function layoutGraph(graph: DeveloperGraph): { nodes: Node[]; edges: Edge[] } {
		const g = new dagre.graphlib.Graph();
		g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 });
		g.setDefaultEdgeLabel(() => ({}));

		const nodeWidth = 170;
		const nodeHeight = 60;

		for (const n of graph.nodes) {
			g.setNode(n.id, { width: nodeWidth, height: nodeHeight });
		}

		for (const e of graph.edges) {
			g.setEdge(e.source, e.target);
		}

		dagre.layout(g);

		const layoutNodes: Node[] = graph.nodes.map((n) => {
			const pos = g.node(n.id);
			return {
				id: n.id,
				type: 'app',
				position: { x: pos.x - nodeWidth / 2, y: pos.y - nodeHeight / 2 },
				data: {
					displayName: n.displayName,
					status: n.status,
					isSystem: n.isSystem,
					sidecar: n.sidecar
				}
			};
		});

		const nodeStatusMap = new Map(graph.nodes.map((n) => [n.id, n.status]));

		const layoutEdges: Edge[] = graph.edges.map((e, i) => {
			const sourceRunning = nodeStatusMap.get(e.source) === 'running';
			const targetRunning = nodeStatusMap.get(e.target) === 'running';
			return {
				id: `e-${i}`,
				source: e.source,
				target: e.target,
				label: e.label,
				animated: sourceRunning && targetRunning
			};
		});

		return { nodes: layoutNodes, edges: layoutEdges };
	}

	function extractErrorMessage(err: unknown): string {
		if (err && typeof err === 'object' && 'message' in err) {
			return (err as { message: string }).message;
		}
		return 'Failed to load developer graph';
	}

	async function load() {
		try {
			const graph = await fetchDeveloperGraph();
			const layout = layoutGraph(graph);
			nodes = layout.nodes;
			edges = layout.edges;
			error = '';
		} catch (err) {
			error = extractErrorMessage(err);
		}
	}

	onMount(() => {
		load().then(() => { loading = false; });
		const interval = setInterval(load, 5000);
		return () => clearInterval(interval);
	});

</script>

<svelte:head>
	<title>Developer · Bloud</title>
</svelte:head>

<div class="page">
	<header class="page-header">
		<div class="header-content">
			<div>
				<h1>Developer</h1>
				<p class="subtitle">Dependency graph &amp; runtime status</p>
			</div>
		</div>
	</header>

	{#if loading}
		<div class="loading-state">
			<p>Loading graph...</p>
		</div>
	{:else if error}
		<div class="error-message">{error}</div>
	{:else if nodes.length === 0}
		<div class="empty-state">
			<p>No apps installed.</p>
		</div>
	{:else}
		<div class="graph-container">
			<SvelteFlow {nodes} {edges} {nodeTypes} fitView colorMode="light" nodesDraggable={false} nodesConnectable={false} elementsSelectable={false} panOnDrag={false} zoomOnScroll={false} zoomOnPinch={false} zoomOnDoubleClick={false} preventScrolling={false}>
			</SvelteFlow>
		</div>
	{/if}
</div>

<style>
	.page {
		padding: var(--space-2xl) var(--space-xl);
		height: 100%;
		display: flex;
		flex-direction: column;
	}

	.page-header {
		margin-bottom: var(--space-2xl);
		padding-bottom: var(--space-xl);
		border-bottom: 1px solid var(--color-border);
		flex-shrink: 0;
	}

	.header-content {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
	}

	.header-content h1 {
		margin: 0;
		font-size: 1.75rem;
		font-weight: 500;
	}

	.subtitle {
		margin: var(--space-xs) 0 0 0;
		color: var(--color-text-muted);
		font-style: italic;
	}

	.loading-state,
	.empty-state {
		padding: var(--space-xl);
		text-align: center;
		color: var(--color-text-muted);
	}

	.error-message {
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		color: var(--color-error);
		background: rgba(220, 38, 38, 0.05);
		border: 1px solid rgba(220, 38, 38, 0.15);
		border-radius: var(--radius-md);
	}

	.graph-container {
		flex: 1;
		min-height: 400px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		overflow: hidden;
	}

</style>
