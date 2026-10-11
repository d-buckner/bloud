<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { Handle, Position } from '@xyflow/svelte';
	import { statusColor } from '$lib/utils/statusColor';
	import { phaseWord, isFailure, isUnprobed, isWorking, showsPhase } from './phase';

	interface NodeData {
		displayName: string;
		status: string;
		hasOutgoing: boolean;
		hasIncoming: boolean;
		phase?: string;
		reason?: string;
		inFlight?: boolean;
		/** Consecutive resync restarts, once the engine raised the warning. */
		resyncRestarts?: number;
	}

	let { data }: { data: NodeData } = $props();

	const phase = $derived(phaseWord(data.phase, data.status));
	const failed = $derived(isFailure(phase));
	const working = $derived(isWorking(data.inFlight, phase));
	const unprobed = $derived(isUnprobed(data.phase));
	const resync = $derived(data.resyncRestarts ?? 0);

	const label = $derived(
		[
			data.displayName,
			phase,
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
	class="container-node"
	class:failed
	class:working
	role="group"
	aria-label={label}
	title={data.reason || data.status}
>
	<span
		class="status-dot"
		class:unprobed
		style:background={unprobed ? 'transparent' : statusColor(phase)}
	></span>
	<span class="container-name">{data.displayName}</span>
	{#if showsPhase(phase)}
		<span class="phase" class:failed>{phase}</span>
	{/if}
	{#if resync > 0}
		<span class="resync" title={`Restarted ${resync} times in a row by the resync pass`}>
			&#8635;{resync}
		</span>
	{/if}
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

	.container-node.working {
		border-color: var(--color-info);
	}

	.container-node.failed {
		border-color: var(--color-error);
	}

	.status-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	/* Nothing reconciles this node: an outline, not a reading. */
	.status-dot.unprobed {
		box-sizing: border-box;
		border: 1.5px solid var(--color-text-secondary, #57534e);
	}

	.container-name {
		font-size: 0.6875rem;
		color: var(--color-text, #1c1917);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	/* Only a container that is doing something or broken gets here: five chips
	   all reading "running" is five words that say what the dots already said. */
	.phase {
		margin-left: auto;
		flex: 0 0 auto;
		font-size: 0.625rem;
		color: var(--color-text-secondary, #57534e);
		text-transform: lowercase;
	}

	.phase.failed {
		color: var(--color-error);
		font-weight: 600;
	}

	/* The resync warning belongs to the container the engine keeps restarting, not
	   to a panel elsewhere on the page. */
	.resync {
		margin-left: auto;
		flex: 0 0 auto;
		font-size: 0.625rem;
		font-weight: 600;
		color: var(--color-warning, #92400e);
	}

	.phase + .resync {
		margin-left: 6px;
	}

	@media (prefers-reduced-motion: no-preference) {
		.working .status-dot {
			animation: bloud-container-pulse 1.1s ease-in-out infinite;
		}
	}

	@keyframes bloud-container-pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.35;
		}
	}
</style>
