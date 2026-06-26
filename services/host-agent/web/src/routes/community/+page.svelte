<script lang="ts">
	import { onMount } from 'svelte';
	import { SvelteFlow, type Node, type Edge, type NodeTypes } from '@xyflow/svelte';
	import dagre from '@dagrejs/dagre';
	import {
		fetchCommunityGraph,
		type CommunityGraph,
		type CommunityNode
	} from '$lib/clients/sharingClient';
	import PersonNode from './PersonNode.svelte';
	import AppIconNode from './AppIconNode.svelte';
	import FitView from '$lib/components/FitView.svelte';

	import '@xyflow/svelte/dist/style.css';

	const nodeTypes: NodeTypes = {
		person: PersonNode as any,
		app: AppIconNode as any
	};

	let loading = $state(true);
	let error = $state('');
	let nodes = $state<Node[]>([]);
	let edges = $state<Edge[]>([]);
	let graphKey = $state('');
	let hasShares = $state(false);

	const PERSON_WIDTH = 80;
	const PERSON_HEIGHT = 80;
	const APP_WIDTH = 60;
	const APP_HEIGHT = 60;
	const NODE_SEP = 80;
	const RANK_SEP = 120;

	function layoutGraph(graph: CommunityGraph): { nodes: Node[]; edges: Edge[] } {
		const g = new dagre.graphlib.Graph();
		g.setGraph({ rankdir: 'LR', nodesep: NODE_SEP, ranksep: RANK_SEP });
		g.setDefaultEdgeLabel(() => ({}));

		for (const n of graph.nodes) {
			const isApp = n.nodeType === 'app';
			g.setNode(n.id, {
				width: isApp ? APP_WIDTH : PERSON_WIDTH,
				height: isApp ? APP_HEIGHT : PERSON_HEIGHT
			});
		}

		for (const e of graph.edges) {
			g.setEdge(e.source, e.target);
		}

		dagre.layout(g);

		const layoutNodes: Node[] = graph.nodes.map((n: CommunityNode) => {
			const pos = g.node(n.id);
			const isApp = n.nodeType === 'app';
			const w = isApp ? APP_WIDTH : PERSON_WIDTH;
			const h = isApp ? APP_HEIGHT : PERSON_HEIGHT;

			return {
				id: n.id,
				type: n.nodeType,
				position: { x: pos.x - w / 2, y: pos.y - h / 2 },
				data: {
					label: n.label,
					isHost: n.id === '__host__',
					appId: n.appId ?? ''
				}
			};
		});

		const layoutEdges: Edge[] = graph.edges.map((e, i) => ({
			id: `e-${i}`,
			source: e.source,
			target: e.target,
			type: 'default',
			animated: true,
			style: 'stroke: #93c5fd; stroke-width: 2px;'
		}));

		return { nodes: layoutNodes, edges: layoutEdges };
	}

	onMount(async () => {
		try {
			const graph = await fetchCommunityGraph();
			hasShares = graph.edges.length > 0;
			const layout = layoutGraph(graph);
			nodes = layout.nodes;
			edges = layout.edges;
			graphKey = nodes.map((n) => n.id).join(',');
		} catch (err) {
			if (err && typeof err === 'object' && 'message' in err) {
				error = (err as { message: string }).message;
			} else {
				error = 'Failed to load community graph';
			}
		} finally {
			loading = false;
		}
	});
</script>

<svelte:head>
	<title>Community · Bloud</title>
</svelte:head>

<div class="page">
	{#if loading}
		<div class="loading-state">
			<p>Loading community graph...</p>
		</div>
	{:else if error}
		<div class="error-message">{error}</div>
	{:else if !hasShares}
		<div class="empty-state">
			<p>No apps shared yet.</p>
			<p class="hint">Share an app with a guest to see the community graph.</p>
		</div>
	{:else}
		<div class="graph-container">
			<SvelteFlow
				{nodes}
				{edges}
				{nodeTypes}
				fitView
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
				<FitView key={graphKey} />
			</SvelteFlow>
		</div>
	{/if}
</div>

<style>
	.page {
		height: 100vh;
		display: flex;
		flex-direction: column;
	}

	.loading-state,
	.empty-state {
		padding: var(--space-xl);
		text-align: center;
		color: var(--color-text-muted);
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
	}

	.empty-state p {
		margin: 0;
	}

	.hint {
		font-size: 0.875rem;
		margin-top: var(--space-sm) !important;
	}

	.error-message {
		margin: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		color: var(--color-error);
		background: rgba(220, 38, 38, 0.05);
		border: 1px solid rgba(220, 38, 38, 0.15);
		border-radius: var(--radius-md);
	}

	.graph-container {
		flex: 1;
		position: relative;
		overflow: hidden;
	}

	.graph-container :global(.svelte-flow__attribution) {
		display: none;
	}
</style>
