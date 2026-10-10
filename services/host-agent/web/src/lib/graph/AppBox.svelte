<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { Handle, Position } from '@xyflow/svelte';
	import { statusColor } from '$lib/utils/statusColor';
	import { phaseWord, isFailure, isUnprobed, isWorking, showsPhase } from './phase';

	interface NodeData {
		displayName: string;
		status: string;
		isSystem: boolean;
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
			data.isSystem ? 'system app' : '',
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
	class="app-box"
	class:system={data.isSystem}
	class:failed
	class:working
	role="group"
	aria-label={label}
	title={data.reason || undefined}
>
	<div class="box-header">
		<span class="box-name">{data.displayName}</span>
		{#if resync > 0}
			<span class="resync" title={`Restarted ${resync} times in a row by the resync pass`}>
				&#8635;{resync}
			</span>
		{/if}
		{#if data.isSystem}
			<span class="system-tag">system</span>
		{/if}
		<span
			class="status-dot"
			class:unprobed
			style:background={unprobed ? 'transparent' : statusColor(phase)}
		></span>
		{#if showsPhase(phase)}
			<span class="status-text" class:failed>{phase}</span>
		{/if}
	</div>
	{#if data.reason}
		<!-- With no status panel on the page, the box is the only place a failure
		     can be read as a sentence rather than as a red dot. -->
		<span class="box-reason" title={data.reason}>{data.reason}</span>
	{/if}
</div>

{#if data.hasOutgoing}
	<Handle type="source" position={Position.Bottom} />
{/if}

<style>
	.app-box {
		box-sizing: border-box;
		position: relative;
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

	.app-box.working {
		border-color: var(--color-info);
	}

	.app-box.failed {
		border-color: var(--color-error);
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

	/* Nothing reconciles this app: an outline, not a reading. */
	.status-dot.unprobed {
		box-sizing: border-box;
		border: 1.5px solid var(--color-text-secondary, #57534e);
	}

	.status-text {
		flex: 0 0 auto;
		font-size: 0.6875rem;
		color: var(--color-text-secondary, #57534e);
		text-transform: lowercase;
	}

	.status-text.failed {
		color: var(--color-error);
		font-weight: 600;
	}

	/* The same tag the flat node uses, so a system app reads as system whether it
	   is drawn as a box or as a single node. */
	.system-tag {
		flex: 0 0 auto;
		font-size: 0.5625rem;
		color: var(--color-text-secondary, #57534e);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	/* The resync warning lands on the app it names: a node the engine keeps
		restarting is a story about that node, not about the engine. */
	.resync {
		flex: 0 0 auto;
		font-size: 0.6875rem;
		font-weight: 600;
		color: var(--color-warning, #92400e);
	}

	/* Anchored to the bottom of the box, which is the room the layout grew when it
	   sized this box: the containers are absolutely positioned children that start
	   under the header, so a line in normal flow would land underneath the first
	   row of them and be covered up. */
	.box-reason {
		position: absolute;
		bottom: 2px;
		left: 10px;
		right: 10px;
		font-family: var(--font-sans, system-ui);
		font-size: 0.625rem;
		line-height: 14px;
		color: var(--color-error, #991b1b);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	@media (prefers-reduced-motion: no-preference) {
		.working .status-dot {
			animation: bloud-box-pulse 1.1s ease-in-out infinite;
		}
	}

	@keyframes bloud-box-pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.35;
		}
	}
</style>
