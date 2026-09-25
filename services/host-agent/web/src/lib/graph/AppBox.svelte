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
		{#if data.isSystem}
			<span class="system-tag">system</span>
		{/if}
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
		/* The header is the only thing inside a box that competes for width, so
		   the box is the query context for dropping the redundant parts of it. */
		container-type: inline-size;
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
		/* The name gets the room; the status and the system tag keep theirs. Without
		   this the header shrinks every item alike and "Traefik" renders as "Tra". */
		flex: 1 1 auto;
		min-width: 0;
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
		flex: 0 0 auto;
	}

	.status-text {
		flex: 0 0 auto;
		font-size: 0.6875rem;
		color: var(--color-text-muted, #78716c);
		text-transform: lowercase;
	}

	/* The same tag the flat node uses, so a system app reads as system whether it
	   is drawn as a box or as a single node. */
	.system-tag {
		flex: 0 0 auto;
		font-size: 0.5625rem;
		color: var(--color-text-muted, #a8a29e);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	/* A single-container box is only as wide as its node, and the app name is
	   what the reader is looking for there: the status dot still says whether
	   it is running, so the word goes first. */
	@container (max-width: 210px) {
		.status-text {
			display: none;
		}
	}
</style>
