<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { Handle, Position } from '@xyflow/svelte';
	import { statusColor } from '$lib/utils/statusColor';

	interface NodeData {
		displayName: string;
		status: string;
		isSystem: boolean;
		hasOutgoing: boolean;
		hasIncoming: boolean;
	}

	let { data }: { data: NodeData } = $props();
</script>

{#if data.hasIncoming}
	<Handle type="target" position={Position.Top} />
{/if}

<div class="app-box" class:system={data.isSystem}>
	<div class="box-header">
		<span class="box-name">{data.displayName}</span>
		<span class="status-dot" style:background={statusColor(data.status)}></span>
		<span class="status-text">{data.status}</span>
	</div>
</div>

{#if data.hasOutgoing}
	<Handle type="source" position={Position.Bottom} />
{/if}

<style>
	.app-box {
		box-sizing: border-box;
		width: 100%;
		height: 100%;
		border: 1px solid var(--color-border, #e5e5e5);
		border-radius: 10px;
		background: rgba(120, 113, 108, 0.06);
		font-family: var(--font-serif, system-ui);
	}

	.app-box.system {
		border-style: dashed;
	}

	.box-header {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 5px 10px;
	}

	.box-name {
		font-weight: 600;
		font-size: 0.8125rem;
		color: var(--color-text, #1c1917);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.status-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		margin-left: auto;
	}

	.status-text {
		font-size: 0.6875rem;
		color: var(--color-text-muted, #78716c);
		text-transform: lowercase;
	}
</style>
