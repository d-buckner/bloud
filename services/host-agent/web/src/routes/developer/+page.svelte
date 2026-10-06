<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import {
		Background,
		ControlButton,
		Controls,
		Panel,
		SvelteFlow,
		type Edge,
		type Node,
		type NodeTypes
	} from '@xyflow/svelte';
	import { layoutGraph } from '$lib/timeline/graphLayout';
	import { syncGraph } from '$lib/timeline/graphSync';
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

	/** The xyflow fit-view glyph, split per subpath so no line runs past the lint width. */
	const FIT_ICON_PATHS = [
		'M3.692 4.63c0-.53.4-.938.939-.938h5.215V0H4.708C2.13 0 0 2.054 0 4.63v5.216h3.692V4.631z',
		'M27.354 0h-5.2v3.692h5.17c.53 0 .984.4.984.939v5.215H32V4.631A4.624 4.624 0 0027.354 0z',
		'M28.308 24.83c0 .532-.4.94-.939.94h-5.215v3.768h5.215c2.577 0 4.631-2.13 4.631-4.707v-5.139h-3.692v5.139z',
		'M4.631 25.77c-.531 0-.939-.4-.939-.94v-5.138H0v5.139c0 2.577 2.13 4.707 4.708 4.707h5.138V25.77H4.631z'
	];

	let loading = $state(true);
	let error = $state('');
	let nodes = $state<Node[]>([]);
	let edges = $state<Edge[]>([]);

	/** Bumped when the graph changed shape and the view should re-frame itself. */
	let fitToken = $state(0);
	/** The operator panned or zoomed: stop moving their viewport for them. */
	let userAdjusted = $state(false);

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
			// The identity-preserving fold is the whole reason a status change does
			// not read as a page reload: unchanged nodes stay the objects the canvas
			// already measured, and the viewport only moves when the shape moved.
			const sync = syncGraph({ nodes, edges }, layoutGraph(graph));
			error = '';
			if (!sync.changed) return;
			nodes = sync.nodes;
			edges = sync.edges;
			if (sync.structureChanged && !userAdjusted) fitToken += 1;
		} catch (err) {
			error = extractErrorMessage(err);
		} finally {
			inflight = false;
		}
	}

	/**
	 * A programmatic transform has no source event, so a move that arrives with
	 * one came from the operator and is theirs to keep.
	 */
	function handleMove(event: MouseEvent | TouchEvent | null) {
		if (event) userAdjusted = true;
	}

	function resumeFollow() {
		userAdjusted = false;
		fitToken += 1;
	}

	onMount(() => {
		load().then(() => {
			loading = false;
		});
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
	{:else if error && nodes.length === 0}
		<div class="error-message">{error}</div>
	{:else if nodes.length === 0}
		<div class="empty-state">
			<p>No apps installed.</p>
		</div>
	{:else}
		{#if error}
			<!-- A poll that failed after the graph is on screen is a banner, not a
			     teardown: the last good layout stays put. -->
			<div class="error-message">{error}</div>
		{/if}
		<div class="graph-container">
			<SvelteFlow
				{nodes}
				{edges}
				{nodeTypes}
				colorMode="light"
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
				onmove={handleMove}
			>
				<Panel position="top-left">
					<div class="graph-status">
						<span class="pulse" class:paused={userAdjusted}></span>
						{#if userAdjusted}
							<span>Auto-fit paused</span>
							<button class="resume" onclick={resumeFollow}>Resume</button>
						{:else}
							<span>Live</span>
						{/if}
					</div>
				</Panel>

				<Controls position="bottom-left" showFitView={false} showLock={false}>
					<ControlButton
						class="graph-fit-button"
						title={userAdjusted ? 'Fit the graph and follow it again' : 'Fit the graph to this view'}
						aria-label="Fit graph to view"
						onclick={resumeFollow}
					>
						<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 30">
							{#each FIT_ICON_PATHS as d (d)}
								<path {d} />
							{/each}
						</svg>
					</ControlButton>
				</Controls>

				<Background />
				<FitView {fitToken} autoFit={!userAdjusted} />
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

	.graph-status {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 6px 10px;
		border: 1px solid var(--color-border, #e5e5e5);
		border-radius: 999px;
		background: rgba(255, 255, 255, 0.88);
		backdrop-filter: blur(4px);
		font-size: 0.6875rem;
		color: var(--color-text-muted, #78716c);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.pulse {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: #16a34a;
		animation: pulse 2s ease-in-out infinite;
	}

	.pulse.paused {
		background: #9ca3af;
		animation: none;
	}

	@keyframes pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.35;
		}
	}

	.resume {
		border: 0;
		padding: 0;
		background: none;
		font: inherit;
		color: var(--color-accent, #2563eb);
		cursor: pointer;
		text-decoration: underline;
	}
</style>
