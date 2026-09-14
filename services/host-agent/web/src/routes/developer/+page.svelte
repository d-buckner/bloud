<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import { onMount } from 'svelte';
	import { SvelteFlow, type Node, type Edge, type NodeTypes } from '@xyflow/svelte';
	import { layoutGraph } from '$lib/services/graphLayout';
	import {
		fetchDeveloperGraph,
		type DeveloperGraph,
		type GraphNode,
		type OrchestratorStatus
	} from '$lib/clients/developerClient';
	import { parseTimeline } from '$lib/services/convergeTimeline';
	import AppNode from './AppNode.svelte';
	import UserNode from './UserNode.svelte';
	import FitView from '$lib/components/FitView.svelte';

	import '@xyflow/svelte/dist/style.css';

	const nodeTypes: NodeTypes = {
		app: AppNode as any,
		user: UserNode as any
	};

	let loading = $state(true);
	let error = $state('');
	let nodes = $state<Node[]>([]);
	let edges = $state<Edge[]>([]);
	let graphKey = $state('');
	let orchestrator = $state<OrchestratorStatus | undefined>(undefined);

	function timeAgo(isoTime: string): string {
		const diff = Date.now() - new Date(isoTime).getTime();
		if (diff < 1000) return 'just now';
		const seconds = Math.floor(diff / 1000);
		if (seconds < 60) return `${seconds}s ago`;
		const minutes = Math.floor(seconds / 60);
		if (minutes < 60) return `${minutes}m ago`;
		const hours = Math.floor(minutes / 60);
		return `${hours}h ago`;
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
			const layout = layoutGraph(graph, window.location.hostname);
			nodes = layout.nodes;
			edges = layout.edges;
			graphKey = nodes.map((n) => `${n.id}:${n.data?.status ?? ''}`).join(',');
			orchestrator = graph.orchestrator;
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

			{#if orchestrator}
				{@const tl = parseTimeline(orchestrator)}
				<div class="overlay-panel">
					<div class="overlay-header">
						<span class="overlay-title">Orchestrator</span>
						<div class="overlay-status">
							{#if orchestrator.isConverging}
								<span class="status-dot converging"></span>
								<span class="status-label">Converging</span>
							{:else}
								<span class="status-dot idle"></span>
								<span class="status-label">Idle</span>
							{/if}
						</div>
					</div>

					<div class="timeline">
						<!-- Queue -->
						<div class="tl-section">
							<div class="tl-rail">
								<span class="tl-dot" class:tl-dot-active={orchestrator.queueDepth > 0}></span>
								<span class="tl-line"></span>
							</div>
							<div class="tl-content">
								<div class="tl-section-header">
									<span class="tl-label">Queue</span>
									<span class="tl-meta">{orchestrator.queueDepth} pending</span>
								</div>
								{#if tl.recentIntents.length > 0}
									<div class="tl-items">
										{#each tl.recentIntents as intent, i (i)}
											<div class="tl-intent">
												<span class="intent-arrow">&rarr;</span>
												<span class="intent-detail">{intent.detail}</span>
												<span class="tl-time">{timeAgo(intent.time)}</span>
											</div>
										{/each}
									</div>
								{:else}
									<p class="tl-empty">No recent intents</p>
								{/if}
							</div>
						</div>

						<!-- Drain -->
						<div class="tl-section">
							<div class="tl-rail">
								<span class="tl-dot" class:tl-dot-done={tl.drain !== null}></span>
								<span class="tl-line"></span>
							</div>
							<div class="tl-content">
								<div class="tl-section-header">
									<span class="tl-label">Drain</span>
									{#if tl.drain}
										<span class="tl-meta">{timeAgo(tl.drain.time)}</span>
									{/if}
								</div>
								{#if tl.drain}
									<p class="tl-summary"><span class="check">&check;</span> {tl.drain.detail} applied</p>
								{:else}
									<p class="tl-empty">No drain yet</p>
								{/if}
							</div>
						</div>

						<!-- Converge -->
						<div class="tl-section tl-section-last">
							<div class="tl-rail">
								<span class="tl-dot" class:tl-dot-done={tl.hasCycle && !orchestrator.isConverging} class:tl-dot-active={orchestrator.isConverging}></span>
							</div>
							<div class="tl-content">
								<div class="tl-section-header">
									<span class="tl-label">Converge</span>
									{#if tl.convergeDuration}
										<span class="tl-meta">{tl.convergeDuration}</span>
									{:else if orchestrator.isConverging}
										<span class="tl-meta tl-meta-active">running...</span>
									{/if}
								</div>
								{#if tl.hasCycle}
									<div class="tl-steps">
										{#each tl.steps as step (step.name)}
											<div class="step" class:step-done={step.status === 'done'} class:step-active={step.status === 'active'} class:step-pending={step.status === 'pending'}>
												<span class="step-icon">
													{#if step.status === 'done'}
														<span class="check">&check;</span>
													{:else if step.status === 'active'}
														<span class="step-spinner"></span>
													{:else}
														<span class="step-circle"></span>
													{/if}
												</span>
												<span class="step-name">{step.detail || step.name}</span>
											</div>
										{/each}
									</div>
								{:else}
									<p class="tl-empty">No convergence cycles yet</p>
								{/if}
							</div>
						</div>
					</div>
				</div>
			{/if}
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

	/* Floating overlay panel */
	.overlay-panel {
		position: absolute;
		top: 12px;
		right: 12px;
		width: 280px;
		background: rgba(255, 255, 255, 0.92);
		backdrop-filter: blur(8px);
		border: 1px solid var(--color-border, #e5e5e5);
		border-radius: var(--radius-md, 8px);
		z-index: 10;
		overflow: hidden;
	}

	.overlay-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 8px 12px;
		border-bottom: 1px solid var(--color-border, #e5e5e5);
	}

	.overlay-title {
		font-size: 0.6875rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted, #78716c);
	}

	.overlay-status {
		display: flex;
		align-items: center;
		gap: 5px;
	}

	.status-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-text-muted, #78716c);
	}

	.status-dot.idle {
		background: #16a34a;
	}

	.status-dot.converging {
		background: #d97706;
		animation: pulse 1.2s ease-in-out infinite;
	}

	.status-label {
		font-weight: 500;
		font-size: 0.75rem;
	}

	/* Timeline layout */
	.timeline {
		padding: 8px 12px;
	}

	.tl-section {
		display: flex;
		gap: 10px;
		min-height: 36px;
	}

	.tl-rail {
		display: flex;
		flex-direction: column;
		align-items: center;
		width: 12px;
		flex-shrink: 0;
		padding-top: 3px;
	}

	.tl-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		border: 1.5px solid var(--color-border, #e5e5e5);
		background: var(--color-bg-elevated, #fff);
		flex-shrink: 0;
		box-sizing: border-box;
	}

	.tl-dot-done {
		border-color: #16a34a;
		background: #16a34a;
	}

	.tl-dot-active {
		border-color: #d97706;
		background: #d97706;
		animation: pulse 1.2s ease-in-out infinite;
	}

	.tl-line {
		flex: 1;
		width: 1.5px;
		background: var(--color-border, #e5e5e5);
		min-height: 8px;
	}

	.tl-content {
		flex: 1;
		min-width: 0;
		padding-bottom: 8px;
	}

	.tl-section-last .tl-content {
		padding-bottom: 0;
	}

	.tl-section-header {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		margin-bottom: 2px;
	}

	.tl-label {
		font-size: 0.6875rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-secondary, #44403c);
	}

	.tl-meta {
		font-size: 0.625rem;
		color: var(--color-text-muted, #78716c);
		font-family: var(--font-mono, monospace);
	}

	.tl-meta-active {
		color: #d97706;
	}

	.tl-empty {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-muted, #78716c);
	}

	.tl-summary {
		margin: 0;
		font-size: 0.75rem;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-secondary, #44403c);
	}

	.check {
		color: #16a34a;
	}

	/* Intent items */
	.tl-items {
		display: flex;
		flex-direction: column;
		gap: 1px;
	}

	.tl-intent {
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: 0.75rem;
		font-family: var(--font-mono, monospace);
	}

	.intent-arrow {
		color: #3b82f6;
		flex-shrink: 0;
	}

	.intent-detail {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--color-text-secondary, #44403c);
	}

	.tl-time {
		flex-shrink: 0;
		font-size: 0.5625rem;
		color: var(--color-text-muted, #78716c);
	}

	/* Converge steps */
	.tl-steps {
		display: flex;
		flex-direction: column;
		gap: 1px;
	}

	.step {
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: 0.75rem;
		font-family: var(--font-mono, monospace);
		height: 18px;
	}

	.step-icon {
		width: 12px;
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		font-size: 0.6875rem;
	}

	.step-done .step-name {
		color: var(--color-text-secondary, #44403c);
	}

	.step-active .step-name {
		color: var(--color-text, #1c1917);
		font-weight: 500;
	}

	.step-pending .step-name {
		color: var(--color-text-muted, #78716c);
	}

	.step-circle {
		width: 5px;
		height: 5px;
		border-radius: 50%;
		border: 1.5px solid var(--color-border, #e5e5e5);
		box-sizing: border-box;
	}

	.step-spinner {
		width: 7px;
		height: 7px;
		border: 1.5px solid var(--color-border, #e5e5e5);
		border-top-color: #d97706;
		border-radius: 50%;
		animation: spin 0.6s linear infinite;
	}

	@keyframes pulse {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.4; }
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}
</style>
