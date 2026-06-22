<script lang="ts">
	import { Handle, Position } from '@xyflow/svelte';
	import type { SidecarStatus } from '$lib/clients/developerClient';

	interface NodeData {
		displayName: string;
		status: string;
		isSystem: boolean;
		sidecar: SidecarStatus | null;
	}

	let { data }: { data: NodeData } = $props();

	function statusColor(status: string): string {
		switch (status) {
			case 'running': return '#16a34a';
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

	function sidecarColor(state: string): string {
		if (state === 'running') return '#16a34a';
		return '#dc2626';
	}
</script>

<Handle type="target" position={Position.Top} />

<div class="app-node" class:system={data.isSystem}>
	<div class="node-header">
		<span class="node-name">{data.displayName}</span>
		{#if data.sidecar}
			<span class="sidecar-badge" style="background: {sidecarColor(data.sidecar.state)}" title="Tailscale sidecar: {data.sidecar.status}">TS</span>
		{/if}
	</div>
	<div class="node-footer">
		<span class="status-dot" style="background: {statusColor(data.status)}"></span>
		<span class="status-text">{data.status}</span>
		{#if data.isSystem}
			<span class="system-tag">system</span>
		{/if}
	</div>
</div>

<Handle type="source" position={Position.Bottom} />

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

	.sidecar-badge {
		font-size: 0.5625rem;
		font-weight: 700;
		color: white;
		padding: 1px 4px;
		border-radius: 3px;
		line-height: 1;
		letter-spacing: 0.02em;
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

	.system-tag {
		margin-left: auto;
		font-size: 0.5625rem;
		color: var(--color-text-muted, #a8a29e);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}
</style>
