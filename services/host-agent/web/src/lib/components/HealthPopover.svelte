<script lang="ts">
	import Icon from './Icon.svelte';

	interface Props {
		label: string;
		value: number;
		thresholds: { warning: number; critical: number };
		suggestions?: { warning: string; critical: string };
	}

	let { label, value, thresholds, suggestions }: Props = $props();

	let isOpen = $state(false);
	let popoverRef = $state<HTMLDivElement | null>(null);
	let triggerRef = $state<HTMLButtonElement | null>(null);

	let status = $derived(
		value >= thresholds.critical ? 'critical' :
		value >= thresholds.warning ? 'warning' : 'good'
	);

	let statusLabel = $derived(
		status === 'critical' ? 'Critical' :
		status === 'warning' ? 'Warning' : 'Healthy'
	);

	let suggestion = $derived(
		status === 'critical' ? suggestions?.critical :
		status === 'warning' ? suggestions?.warning : null
	);

	function toggle() {
		isOpen = !isOpen;
	}

	function handleClickOutside(e: MouseEvent) {
		if (isOpen && popoverRef && !popoverRef.contains(e.target as Node) && !triggerRef?.contains(e.target as Node)) {
			isOpen = false;
		}
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && isOpen) {
			isOpen = false;
		}
	}

	// $effect only runs on the client, avoiding SSR issues with document
	$effect(() => {
		document.addEventListener('click', handleClickOutside);
		document.addEventListener('keydown', handleKeydown);

		return () => {
			document.removeEventListener('click', handleClickOutside);
			document.removeEventListener('keydown', handleKeydown);
		};
	});
</script>

<div class="health-popover-container">
	<button
		bind:this={triggerRef}
		class="health-trigger"
		onclick={toggle}
		aria-expanded={isOpen}
		aria-label="{label}: {value}%"
	>
		<span class="health-dot {status}"></span>
		<span class="health-label">{label}</span>
	</button>

	{#if isOpen}
		<div bind:this={popoverRef} class="popover">
			<div class="popover-header">
				<span class="popover-label">{label}</span>
				<span class="popover-status {status}">{statusLabel}</span>
			</div>

			<div class="popover-value">
				<span class="value-number">{value}%</span>
				<div class="value-bar">
					<div
						class="value-fill {status}"
						style="width: {Math.min(value, 100)}%"
					></div>
				</div>
			</div>

			{#if suggestion}
				<div class="popover-suggestion">
					<Icon name="info" size={14} />
					<span>{suggestion}</span>
				</div>
			{/if}
		</div>
	{/if}
</div>

<style>
	.health-popover-container {
		position: relative;
	}

	.health-trigger {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 4px 8px;
		margin: -4px -8px;
		background: transparent;
		border: none;
		border-radius: var(--radius-sm);
		cursor: pointer;
		transition: background 0.1s ease;
	}

	.health-trigger:hover {
		background: var(--color-bg-subtle);
	}

	.health-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--color-text-muted);
		flex-shrink: 0;
	}

	.health-dot.good { background: var(--color-success); }
	.health-dot.warning { background: #F59E0B; }
	.health-dot.critical { background: var(--color-error); }

	.health-label {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.popover {
		position: absolute;
		top: calc(100% + 8px);
		right: 0;
		width: 220px;
		padding: var(--space-md);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-lg);
		z-index: 100;
		animation: popoverIn 0.15s ease;
	}

	@keyframes popoverIn {
		from { opacity: 0; transform: translateY(-4px); }
		to { opacity: 1; transform: translateY(0); }
	}

	.popover-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: var(--space-sm);
	}

	.popover-label {
		font-weight: 500;
		font-size: 0.875rem;
	}

	.popover-status {
		font-size: 0.6875rem;
		padding: 2px 6px;
		border-radius: 4px;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		font-weight: 500;
	}

	.popover-status.good {
		background: var(--color-success-bg);
		color: var(--color-success);
	}

	.popover-status.warning {
		background: rgba(245, 158, 11, 0.1);
		color: #B45309;
	}

	.popover-status.critical {
		background: rgba(185, 28, 28, 0.1);
		color: var(--color-error);
	}

	.popover-value {
		margin-bottom: var(--space-sm);
	}

	.value-number {
		font-size: 1.25rem;
		font-weight: 500;
		font-family: var(--font-mono);
	}

	.value-bar {
		height: 4px;
		background: var(--color-border);
		border-radius: 2px;
		margin-top: var(--space-xs);
		overflow: hidden;
	}

	.value-fill {
		height: 100%;
		border-radius: 2px;
		transition: width 0.3s ease;
	}

	.value-fill.good { background: var(--color-success); }
	.value-fill.warning { background: #F59E0B; }
	.value-fill.critical { background: var(--color-error); }

	.popover-suggestion {
		display: flex;
		align-items: flex-start;
		gap: var(--space-sm);
		padding: var(--space-sm);
		background: var(--color-bg-subtle);
		border-radius: var(--radius-sm);
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		line-height: 1.4;
	}

	.popover-suggestion :global(svg) {
		flex-shrink: 0;
		margin-top: 1px;
		color: var(--color-text-muted);
	}
</style>
