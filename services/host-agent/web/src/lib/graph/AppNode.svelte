<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { Handle, Position } from '@xyflow/svelte';
	import { statusColor } from '$lib/utils/statusColor';
	import { phaseWord, isFailure, isUnprobed, isWorking, showsPhase } from './phase';

	interface NodeData {
		displayName: string;
		status: string;
		isSystem: boolean;
		nodeType: string;
		hasOutgoing: boolean;
		hasIncoming: boolean;
		phase?: string;
		reason?: string;
		inFlight?: boolean;
		/** Consecutive resync restarts, once the engine raised the warning. */
		resyncRestarts?: number;
	}

	let { data }: { data: NodeData } = $props();

	const isConnection = $derived(data.nodeType === 'connection');
	const phase = $derived(phaseWord(data.phase, data.status));
	const failed = $derived(isFailure(phase));
	const working = $derived(isWorking(data.inFlight, phase));
	const unprobed = $derived(isUnprobed(data.phase));
	const resync = $derived(data.resyncRestarts ?? 0);

	// The reason is the engine's own words about why a node stopped. There is no
	// status panel to spell it out, so the card carries it, truncated, and the
	// tooltip and accessible name carry it in full.
	const label = $derived(
		[
			data.displayName,
			phase,
			data.isSystem ? 'system app' : '',
			isConnection ? 'public address' : '',
			data.reason ? `stopped: ${data.reason}` : '',
			resync > 0 ? `restarted ${resync} times from resync` : ''
		]
			.filter(Boolean)
			.join(', ')
	);
</script>

{#if data.hasIncoming}
	<Handle type="target" position={Position.Top} />
{/if}

<div
	class="app-node"
	class:system={data.isSystem}
	class:failed
	class:working
	role="group"
	aria-label={label}
	title={data.reason || undefined}
>
	<div class="node-header">
		<span class="node-name">{data.displayName}</span>
		{#if resync > 0}
			<span class="resync" title={`Restarted ${resync} times in a row by the resync pass`}>
				&#8635;{resync}
			</span>
		{/if}
		{#if failed}
			<span class="flag" aria-hidden="true">!</span>
		{/if}
	</div>
	<div class="node-footer">
		<span class="status-dot" class:unprobed style:background={unprobed ? 'transparent' : statusColor(phase)}></span>
		{#if showsPhase(phase)}
			<span class="status-text" class:failed>{phase}</span>
		{/if}
		{#if data.isSystem}
			<span class="kind-tag">system</span>
		{:else if isConnection}
			<span class="kind-tag">address</span>
		{/if}
	</div>
	{#if data.reason}
		<span class="node-reason" title={data.reason}>{data.reason}</span>
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

	/* The two states worth spotting from across the canvas. Both are drawn on the
	   frame rather than only on the dot, because the dot is 7px and a pass moving
	   through a graph is the thing a reader is trying to follow. */
	.app-node.working {
		border-color: var(--color-info);
		box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-info) 28%, transparent);
	}

	.app-node.failed {
		border-color: var(--color-error);
		box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-error) 22%, transparent);
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

	/* Nothing reconciles this node, so the dot is an outline: filled means the
	   engine has a state for it, hollow means the card is only reporting what the
	   catalog said. */
	.status-dot.unprobed {
		box-sizing: border-box;
		border: 1.5px solid var(--color-text-secondary, #57534e);
	}

	.status-text {
		font-size: 0.6875rem;
		/* --color-text-secondary, not --color-text-muted: the muted token is
		   #A8A29E, which is 2.5:1 on white and unreadable as 11px text. */
		color: var(--color-text-secondary, #57534e);
		text-transform: lowercase;
	}

	.status-text.failed {
		color: var(--color-error);
		font-weight: 600;
	}

	.kind-tag {
		margin-left: auto;
		font-size: 0.5625rem;
		color: var(--color-text-secondary, #57534e);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.flag {
		font-size: 0.6875rem;
		font-weight: 700;
		line-height: 1;
		color: var(--color-error);
	}

	.resync {
		font-size: 0.6875rem;
		font-weight: 600;
		line-height: 1;
		color: var(--color-warning, #92400e);
	}

	/* One line of the engine's own sentence, under the state it explains. The card
	   grows past its layout height by the line; the gap below it is 72px, so the
	   line eats margin and never a neighbour. */
	.node-reason {
		display: block;
		margin-top: 4px;
		font-family: var(--font-sans, system-ui);
		font-size: 0.625rem;
		line-height: 14px;
		max-width: 190px;
		color: var(--color-error, #991b1b);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	@media (prefers-reduced-motion: no-preference) {
		.working .status-dot {
			animation: bloud-phase-pulse 1.1s ease-in-out infinite;
		}
	}

	@keyframes bloud-phase-pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.35;
		}
	}
</style>
