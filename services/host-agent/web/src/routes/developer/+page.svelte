<script lang="ts">
	import { onMount } from 'svelte';
	import { SvelteFlow, type Node, type Edge, type NodeTypes } from '@xyflow/svelte';
	import dagre from '@dagrejs/dagre';
	import { fetchDeveloperGraph, type DeveloperGraph, type GraphNode } from '$lib/clients/developerClient';
	import AppNode from './AppNode.svelte';
	import FitView from './FitView.svelte';

	import '@xyflow/svelte/dist/style.css';

	const nodeTypes: NodeTypes = {
		app: AppNode as any
	};

	let loading = $state(true);
	let error = $state('');
	let nodes = $state<Node[]>([]);
	let edges = $state<Edge[]>([]);
	let graphKey = $state('');

	const NODE_WIDTH = 170;
	const NODE_HEIGHT = 60;
	const GROUP_PADDING = 40;
	const CONNECTION_GAP = 100;

	function layoutGraph(graph: DeveloperGraph): { nodes: Node[]; edges: Edge[] } {
		const appNodes = graph.nodes.filter((n) => n.nodeType === 'app');
		const connectionNodes = graph.nodes.filter((n) => n.nodeType === 'connection');

		// Edges between app nodes only (for dagre internal layout)
		const appNodeIds = new Set(appNodes.map((n) => n.id));
		const appEdges = graph.edges.filter((e) => appNodeIds.has(e.source) && appNodeIds.has(e.target));

		// Layout app nodes with dagre
		const g = new dagre.graphlib.Graph();
		g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 });
		g.setDefaultEdgeLabel(() => ({}));

		for (const n of appNodes) {
			g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT });
		}
		for (const e of appEdges) {
			g.setEdge(e.source, e.target);
		}

		dagre.layout(g);

		// Compute bounding box of app nodes (dagre centers)
		let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
		for (const n of appNodes) {
			const pos = g.node(n.id);
			minX = Math.min(minX, pos.x - NODE_WIDTH / 2);
			minY = Math.min(minY, pos.y - NODE_HEIGHT / 2);
			maxX = Math.max(maxX, pos.x + NODE_WIDTH / 2);
			maxY = Math.max(maxY, pos.y + NODE_HEIGHT / 2);
		}

		const sources = new Set(graph.edges.map((e) => e.source));
		const targets = new Set(graph.edges.map((e) => e.target));

		function nodeData(n: GraphNode) {
			return {
				displayName: n.displayName,
				status: n.status,
				isSystem: n.isSystem,
				nodeType: n.nodeType,
				hasOutgoing: sources.has(n.id),
				hasIncoming: targets.has(n.id)
			};
		}

		const layoutNodes: Node[] = [];

		if (appNodes.length > 0) {
			const groupWidth = maxX - minX + GROUP_PADDING * 2;
			const groupHeight = maxY - minY + GROUP_PADDING * 2;
			const groupX = minX - GROUP_PADDING;
			const groupY = minY - GROUP_PADDING;

			// Group node for apps
			layoutNodes.push({
				id: '__apps_group',
				type: 'group',
				position: { x: groupX, y: groupY },
				style: `width: ${groupWidth}px; height: ${groupHeight}px;`,
				data: {}
			});

			// App nodes inside the group (positions relative to group)
			for (const n of appNodes) {
				const pos = g.node(n.id);
				layoutNodes.push({
					id: n.id,
					type: 'app',
					position: { x: pos.x - NODE_WIDTH / 2 - groupX, y: pos.y - NODE_HEIGHT / 2 - groupY },
					parentId: '__apps_group',
					data: nodeData(n)
				});
			}

			// Connection nodes positioned above the group, spread horizontally
			const connY = groupY - NODE_HEIGHT - CONNECTION_GAP;
			const totalConnWidth = connectionNodes.length * NODE_WIDTH + (connectionNodes.length - 1) * 60;
			const connStartX = groupX + groupWidth / 2 - totalConnWidth / 2;

			for (let i = 0; i < connectionNodes.length; i++) {
				const cn = connectionNodes[i];
				layoutNodes.push({
					id: cn.id,
					type: 'app',
					position: { x: connStartX + i * (NODE_WIDTH + 60), y: connY },
					data: nodeData(cn)
				});
			}
		} else {
			// No app nodes — just lay out connection nodes horizontally
			for (let i = 0; i < connectionNodes.length; i++) {
				const cn = connectionNodes[i];
				layoutNodes.push({
					id: cn.id,
					type: 'app',
					position: { x: i * (NODE_WIDTH + 60), y: 0 },
					data: nodeData(cn)
				});
			}
		}

		const nodeStatusMap = new Map(graph.nodes.map((n) => [n.id, n.status]));

		const layoutEdges: Edge[] = graph.edges.map((e, i) => {
			const sourceStatus = nodeStatusMap.get(e.source) ?? '';
			const targetStatus = nodeStatusMap.get(e.target) ?? '';
			const sourceActive = sourceStatus === 'running' || sourceStatus === 'active';
			const targetActive = targetStatus === 'running' || targetStatus === 'active';
			return {
				id: `e-${i}`,
				source: e.source,
				target: e.target,
				label: e.label,
				animated: sourceActive && targetActive
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

	let inflight = false;

	async function load() {
		if (inflight) return;
		inflight = true;
		try {
			const graph = await fetchDeveloperGraph();
			const layout = layoutGraph(graph);
			nodes = layout.nodes;
			edges = layout.edges;
			graphKey = nodes.map((n) => `${n.id}:${n.data?.status ?? ''}`).join(',');
			error = '';
		} catch (err) {
			error = extractErrorMessage(err);
		} finally {
			inflight = false;
		}
	}

	onMount(() => {
		load().then(() => { loading = false; });
		const interval = setInterval(load, 500);
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
				<FitView key={graphKey} />
			</SvelteFlow>
		</div>
	{/if}
</div>

<style>
	.page {
		padding: var(--space-2xl) var(--space-xl);
		height: 100vh;
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

	.graph-container :global(.svelte-flow__attribution) {
		display: none;
	}

</style>
