<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { Handle, Position } from '@xyflow/svelte';
	import { statusColor } from '$lib/utils/statusColor';

	interface NodeData {
		displayName: string;
		status: string;
		hasOutgoing: boolean;
		hasIncoming: boolean;
	}

	let { data }: { data: NodeData } = $props();
</script>

{#if data.hasIncoming}
	<Handle type="target" position={Position.Top} />
{/if}

<div class="container-node" title={data.status}>
	<span class="status-dot" style:background={statusColor(data.status)}></span>
	<span class="container-name">{data.displayName}</span>
</div>

{#if data.hasOutgoing}
	<Handle type="source" position={Position.Bottom} />
{/if}

<style>
	.container-node {
		box-sizing: border-box;
		display: flex;
		align-items: center;
		gap: 6px;
		width: 100%;
		height: 100%;
		padding: 0 10px;
		border: 1px solid var(--color-border, #e5e5e5);
		border-radius: 6px;
		background: var(--color-bg-elevated, #fff);
		font-family: var(--font-mono, monospace);
	}

	.status-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.container-name {
		font-size: 0.6875rem;
		color: var(--color-text, #1c1917);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
