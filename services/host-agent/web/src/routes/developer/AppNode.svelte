<script lang="ts">
	import { Handle, Position } from '@xyflow/svelte';

	interface PhaseProgress {
		phase: string;
		status: string;
		progress: number;
		total: number;
		phases: { name: string; status: 'active' | 'done' | 'error' | 'warning' | 'pending' }[];
	}

	interface NodeData {
		displayName: string;
		status: string;
		isSystem: boolean;
		nodeType: string;
		hasOutgoing: boolean;
		hasIncoming: boolean;
		currentPhase?: PhaseProgress;
	}

	let { data }: { data: NodeData } = $props();

	function statusColor(status: string): string {
		switch (status) {
			case 'running': return '#16a34a';
			case 'active': return '#16a34a';
			case 'error':
			case 'exited':
			case 'dead': return '#dc2626';
			case 'healthcheck': return '#eab308';
			case 'prestart':
			case 'poststart': return '#3b82f6';
			case 'queued':
			case 'installing': return '#9ca3af';
			default: return '#9ca3af';
		}
	}

	function segmentColor(status: string): string {
		switch (status) {
			case 'done': return '#16a34a';
			case 'active': return '#d97706';
			case 'error': return '#dc2626';
			case 'warning': return '#d97706';
			default: return 'transparent';
		}
	}

	const isConnection = $derived(data.nodeType === 'connection');
	const label = $derived(data.displayName || window.location.hostname);
	const hasPhase = $derived(!!data.currentPhase);
</script>

{#if data.hasIncoming}
	<Handle type="target" position={Position.Top} />
{/if}

<div class="app-node" class:system={data.isSystem} class:connection={isConnection}>
	<div class="node-header">
		<span class="node-name">{label}</span>
	</div>
	<div class="node-footer">
		{#if hasPhase}
			<span class="phase-spinner"></span>
			<span class="status-text phase-text">{data.currentPhase?.phase}</span>
		{:else}
			<span class="status-dot" style="background: {statusColor(data.status)}"></span>
			<span class="status-text">{data.status}</span>
		{/if}
		{#if data.isSystem}
			<span class="system-tag">system</span>
		{/if}
		{#if isConnection}
			<span class="system-tag">connection</span>
		{/if}
	</div>
	{#if data.currentPhase}
		<div class="phase-bar">
			{#each data.currentPhase.phases as seg}
				<span
					class="phase-seg"
					class:phase-seg-active={seg.status === 'active'}
					style="background: {segmentColor(seg.status)}; {seg.status === 'pending' ? 'border: 1px solid #d6d3d1;' : ''}"
				></span>
			{/each}
		</div>
	{/if}
</div>

{#if data.hasOutgoing}
	<Handle type="source" position={Position.Bottom} />
{/if}

<style>
	.app-node {
		padding: 10px 14px;
		border-radius: 8px;
		background: var(--color-bg-elevated, #fff);
		border: 1px solid var(--color-border, #e5e5e5);
		min-width: 140px;
		font-family: var(--font-serif, system-ui);
	}

	.app-node.system {
		border-style: dashed;
	}

	.app-node.connection {
		border-color: #6366f1;
		border-width: 2px;
		background: #eef2ff;
	}

	.node-header {
		display: flex;
		align-items: center;
		gap: 6px;
		margin-bottom: 6px;
	}

	.node-name {
		font-weight: 600;
		font-size: 0.8125rem;
		color: var(--color-text, #1c1917);
	}

	.node-footer {
		display: flex;
		align-items: center;
		gap: 5px;
	}

	.status-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.status-text {
		font-size: 0.6875rem;
		color: var(--color-text-muted, #78716c);
		text-transform: lowercase;
	}

	.phase-text {
		font-family: var(--font-mono, monospace);
		color: var(--color-text, #1c1917);
	}

	.system-tag {
		margin-left: auto;
		font-size: 0.5625rem;
		color: var(--color-text-muted, #a8a29e);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.phase-spinner {
		width: 8px;
		height: 8px;
		border: 1.5px solid var(--color-border, #e5e5e5);
		border-top-color: #d97706;
		border-radius: 50%;
		animation: spin 0.6s linear infinite;
		flex-shrink: 0;
	}

	.phase-bar {
		display: flex;
		gap: 2px;
		margin-top: 6px;
	}

	.phase-seg {
		width: 16px;
		height: 3px;
		border-radius: 1px;
		box-sizing: border-box;
	}

	.phase-seg-active {
		animation: pulse 1.2s ease-in-out infinite;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	@keyframes pulse {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.4; }
	}
</style>
