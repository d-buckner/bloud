<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import { Handle, Position } from '@xyflow/svelte';

	interface NodeData {
		label: string;
		hasOutgoing: boolean;
	}

	let { data }: { data: NodeData } = $props();

	let initial = $derived(data.label.charAt(0).toUpperCase());
</script>

<div class="user-node">
	<div class="avatar">
		<span class="initial">{initial}</span>
	</div>
	<span class="label">{data.label}</span>
</div>

{#if data.hasOutgoing}
	<Handle type="source" position={Position.Bottom} />
{/if}

<style>
	.user-node {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 6px;
	}

	.avatar {
		width: 48px;
		height: 48px;
		border-radius: 50%;
		background: #dbeafe;
		display: flex;
		align-items: center;
		justify-content: center;
		border: 2px solid #93c5fd;
	}

	.initial {
		font-size: 1.25rem;
		font-weight: 600;
		color: #2563eb;
	}

	.label {
		font-size: 0.6875rem;
		font-weight: 500;
		color: #44403c;
		text-align: center;
		white-space: nowrap;
	}
</style>
