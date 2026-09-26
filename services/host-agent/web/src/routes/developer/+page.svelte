<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import { SvelteFlow, type Node, type Edge, type NodeTypes } from '@xyflow/svelte';
	import { layoutGraph } from '$lib/timeline/graphLayout';
	import { fetchDeveloperGraph } from '$lib/clients/developerClient';
	import AppNode from '$lib/graph/AppNode.svelte';
	import AppBox from '$lib/graph/AppBox.svelte';
	import ContainerNode from '$lib/graph/ContainerNode.svelte';
	import UserNode from './UserNode.svelte';
	import FitView from '$lib/components/FitView.svelte';

	import '@xyflow/svelte/dist/style.css';

	const nodeTypes: NodeTypes = {
		app: AppNode as any,
		appBox: AppBox as any,
		container: ContainerNode as any,
		user: UserNode as any
	};

	let loading = $state(true);
	let error = $state('');
	let nodes = $state<Node[]>([]);
	let edges = $state<Edge[]>([]);
	let graphKey = $state('');

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
			const layout = layoutGraph(graph, window.location.hostname);
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
		align-items: center;
		justify-content: center;
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
