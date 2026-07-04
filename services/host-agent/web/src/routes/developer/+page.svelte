<script lang="ts">
	import { onMount } from 'svelte';
	import { SvelteFlow, type Node, type Edge, type NodeTypes } from '@xyflow/svelte';
	import dagre from '@dagrejs/dagre';
	import {
		fetchDeveloperGraph,
		type DeveloperGraph,
		type GraphNode,
		type ReconcilerStatus,
		type AppPhase
	} from '$lib/clients/developerClient';
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
	let reconciler = $state<ReconcilerStatus | undefined>(undefined);

	const NODE_WIDTH = 170;
	const NODE_HEIGHT = 60;
	const USER_NODE_SIZE = 64;
	const GROUP_PADDING = 40;
	const CONNECTION_GAP = 100;
	const USER_GAP = 60;

	const APP_PHASES = ['pre-start', 'ensure-container', 'health-check', 'post-start', 'sso'];

	const CONVERGE_STEPS = [
		'sync-container-state',
		'handle-uninstalls',
		'compute-levels',
		'ensure-apps',
		'converge-tailnet',
		'update-graph',
		'regenerate-routes'
	];

	interface TimelineStep {
		name: string;
		status: 'done' | 'active' | 'pending';
		detail: string;
	}

	interface Timeline {
		recentIntents: { detail: string; time: string }[];
		drain: { detail: string; time: string } | null;
		steps: TimelineStep[];
		convergeDuration: string | null;
		convergeTime: string | null;
		hasCycle: boolean;
	}

	function parseTimeline(status: ReconcilerStatus): Timeline {
		const activity = status.recentActivity;

		const recentIntents = activity
			.filter((a) => a.event === 'intent_enqueued')
			.slice(0, 5)
			.map((a) => ({ detail: a.detail, time: a.time }));

		const lastDrain = activity.find((a) => a.event === 'drain_complete');
		const drain = lastDrain ? { detail: lastDrain.detail, time: lastDrain.time } : null;

		const completedSteps = new Map<string, string>();
		let cycleComplete = false;
		let convergeDuration: string | null = null;
		let convergeTime: string | null = null;
		let hasCycle = false;

		for (const entry of activity) {
			if (entry.event === 'converge_complete') {
				cycleComplete = true;
				const parts = entry.detail.split(', ');
				convergeDuration = parts.length > 1 ? parts[parts.length - 1] : null;
				convergeTime = entry.time;
				continue;
			}
			if (entry.event === 'converge_start') {
				hasCycle = true;
				break;
			}
			if (entry.event === 'converge_step') {
				hasCycle = true;
				const stepName = entry.detail.split(' (')[0];
				completedSteps.set(stepName, entry.detail);
			}
		}

		const steps: TimelineStep[] = CONVERGE_STEPS.map((name) => {
			if (completedSteps.has(name)) {
				return { name, status: 'done' as const, detail: completedSteps.get(name)! };
			}
			return { name, status: 'pending' as const, detail: '' };
		});

		if (status.isConverging && !cycleComplete) {
			const firstPending = steps.find((s) => s.status === 'pending');
			if (firstPending) {
				firstPending.status = 'active';
			}
		}

		if (cycleComplete) {
			for (const step of steps) {
				if (step.status === 'pending') {
					step.status = 'done';
				}
			}
		}

		return { recentIntents, drain, steps, convergeDuration, convergeTime, hasCycle };
	}

	function buildPhaseData(appName: string, appPhases: AppPhase[] | undefined) {
		if (!appPhases || appPhases.length === 0) return undefined;

		const phaseMap = new Map<string, AppPhase['status']>();
		for (const ap of appPhases) {
			if (ap.appName === appName) {
				phaseMap.set(ap.phase, ap.status);
			}
		}

		if (phaseMap.size === 0) return undefined;

		const phases = APP_PHASES.map((name) => ({
			name,
			status: (phaseMap.get(name) ?? 'pending') as 'active' | 'done' | 'error' | 'warning' | 'pending'
		}));

		// All phases done — convergence complete, hide the bar.
		if (phases.every((p) => p.status === 'done')) return undefined;

		const activePhase = phases.find((p) => p.status === 'active');
		const errorPhase = phases.find((p) => p.status === 'error' || p.status === 'warning');
		const currentPhase = activePhase ?? errorPhase;
		const doneCount = phases.filter((p) => p.status === 'done').length;

		return {
			phase: currentPhase?.name ?? APP_PHASES[doneCount] ?? 'done',
			status: currentPhase?.status ?? 'done',
			progress: doneCount,
			total: APP_PHASES.length,
			phases
		};
	}

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

	function detectUserConnection(graph: DeveloperGraph): string | null {
		const hostname = window.location.hostname;
		const tailnetDomain = graph.tailnetDomain;

		// If the hostname matches the tailnet domain, user is on the tailnet
		if (tailnetDomain && hostname.endsWith(tailnetDomain)) {
			const tailnetConn = graph.nodes.find((n) => n.id.startsWith('conn:tailnet:'));
			if (tailnetConn) return tailnetConn.id;
		}

		// Otherwise user is on LAN
		const localConn = graph.nodes.find((n) => n.id === 'conn:local');
		if (localConn) return localConn.id;

		return null;
	}

	function layoutGraph(graph: DeveloperGraph): { nodes: Node[]; edges: Edge[] } {
		const appNodes = graph.nodes.filter((n) => n.nodeType === 'app');
		const connectionNodes = graph.nodes.filter((n) => n.nodeType === 'connection');

		const appNodeIds = new Set(appNodes.map((n) => n.id));
		const appEdges = graph.edges.filter((e) => appNodeIds.has(e.source) && appNodeIds.has(e.target));

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

		// Detect which connection the current user is reaching through
		const userConnectionId = detectUserConnection(graph);

		// Include the "You" node in source/target tracking so handles render
		if (userConnectionId) {
			sources.add('__you__');
			targets.add(userConnectionId);
		}

		function nodeData(n: GraphNode) {
			const phaseData = buildPhaseData(n.id, graph.reconciler?.appPhases);
			return {
				displayName: n.displayName,
				status: n.status,
				isSystem: n.isSystem,
				nodeType: n.nodeType,
				hasOutgoing: sources.has(n.id),
				hasIncoming: targets.has(n.id),
				currentPhase: phaseData
			};
		}

		const layoutNodes: Node[] = [];

		if (appNodes.length > 0) {
			const groupWidth = maxX - minX + GROUP_PADDING * 2;
			const groupHeight = maxY - minY + GROUP_PADDING * 2;
			const groupX = minX - GROUP_PADDING;
			const groupY = minY - GROUP_PADDING;

			layoutNodes.push({
				id: '__apps_group',
				type: 'group',
				position: { x: groupX, y: groupY },
				style: `width: ${groupWidth}px; height: ${groupHeight}px;`,
				data: {}
			});

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

			// Add "You" node above the connection the user is accessing through
			if (userConnectionId && connectionNodes.length > 0) {
				const connIndex = connectionNodes.findIndex((cn) => cn.id === userConnectionId);
				const idx = connIndex >= 0 ? connIndex : 0;
				const connX = connStartX + idx * (NODE_WIDTH + 60);
				const userX = connX + NODE_WIDTH / 2 - USER_NODE_SIZE / 2;
				const userY = connY - USER_NODE_SIZE - USER_GAP;

				layoutNodes.push({
					id: '__you__',
					type: 'user',
					position: { x: userX, y: userY },
					data: { label: 'You', hasOutgoing: true }
				});
			}
		} else {
			for (let i = 0; i < connectionNodes.length; i++) {
				const cn = connectionNodes[i];
				layoutNodes.push({
					id: cn.id,
					type: 'app',
					position: { x: i * (NODE_WIDTH + 60), y: 0 },
					data: nodeData(cn)
				});
			}

			// Add "You" node when only connections exist
			if (userConnectionId && connectionNodes.length > 0) {
				const connIndex = connectionNodes.findIndex((cn) => cn.id === userConnectionId);
				const idx = connIndex >= 0 ? connIndex : 0;
				const connX = idx * (NODE_WIDTH + 60);
				const userX = connX + NODE_WIDTH / 2 - USER_NODE_SIZE / 2;

				layoutNodes.push({
					id: '__you__',
					type: 'user',
					position: { x: userX, y: -(USER_NODE_SIZE + USER_GAP) },
					data: { label: 'You', hasOutgoing: true }
				});
			}
		}

		const nodeStatusMap = new Map(graph.nodes.map((n) => [n.id, n.status]));
		// Add "You" as always active for edge animation
		nodeStatusMap.set('__you__', 'active');

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

		// Add edge from "You" to the active connection
		if (userConnectionId) {
			const connStatus = nodeStatusMap.get(userConnectionId) ?? '';
			const connActive = connStatus === 'running' || connStatus === 'active';
			layoutEdges.push({
				id: 'e-you',
				source: '__you__',
				target: userConnectionId,
				animated: connActive
			});
		}

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
			reconciler = graph.reconciler;
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

			{#if reconciler}
				{@const tl = parseTimeline(reconciler)}
				<div class="overlay-panel">
					<div class="overlay-header">
						<span class="overlay-title">Reconciler</span>
						<div class="overlay-status">
							{#if reconciler.isConverging}
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
								<span class="tl-dot" class:tl-dot-active={reconciler.queueDepth > 0}></span>
								<span class="tl-line"></span>
							</div>
							<div class="tl-content">
								<div class="tl-section-header">
									<span class="tl-label">Queue</span>
									<span class="tl-meta">{reconciler.queueDepth} pending</span>
								</div>
								{#if tl.recentIntents.length > 0}
									<div class="tl-items">
										{#each tl.recentIntents as intent}
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
								<span class="tl-dot" class:tl-dot-done={tl.hasCycle && !reconciler.isConverging} class:tl-dot-active={reconciler.isConverging}></span>
							</div>
							<div class="tl-content">
								<div class="tl-section-header">
									<span class="tl-label">Converge</span>
									{#if tl.convergeDuration}
										<span class="tl-meta">{tl.convergeDuration}</span>
									{:else if reconciler.isConverging}
										<span class="tl-meta tl-meta-active">running...</span>
									{/if}
								</div>
								{#if tl.hasCycle}
									<div class="tl-steps">
										{#each tl.steps as step}
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
