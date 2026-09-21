<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { clampPercent, formatPercent } from '$lib/utils/format';

	interface Stats {
		cpu: number;
		memory: number;
		disk: number;
	}

	const REFRESH_MS = 10_000;
	/** Backend reading is a 2s-cached percentage, so polling slower is free. */
	const READINGS = ['cpu', 'memory', 'disk'] as const;
	const LABELS: Record<(typeof READINGS)[number], string> = {
		cpu: 'CPU',
		memory: 'Memory',
		disk: 'Disk'
	};

	let stats = $state<Stats | null>(null);
	let failed = $state(false);

	async function refresh() {
		try {
			const response = await fetch('/api/system/status');
			if (!response.ok) throw new Error('request failed');
			stats = await response.json();
			failed = false;
		} catch {
			failed = true;
		}
	}

	$effect(() => {
		refresh();
		const interval = setInterval(refresh, REFRESH_MS);
		return () => clearInterval(interval);
	});

	/** Load colour: green until it matters, amber then red near saturation. */
	function tone(value: number): string {
		if (value >= 90) return 'var(--color-error)';
		if (value >= 75) return 'var(--color-warning)';
		return 'var(--color-success)';
	}
</script>

<div class="system-stats">
	{#if failed}
		<p class="notice error">Host metrics unavailable</p>
	{:else if !stats}
		<p class="notice">Reading…</p>
	{:else}
		{#each READINGS as reading (reading)}
			{@const value = clampPercent(stats[reading])}
			<div class="stat">
				<div class="stat-header">
					<span class="stat-label">{LABELS[reading]}</span>
					<span class="stat-value">{formatPercent(value)}</span>
				</div>
				<div
					class="stat-bar"
					role="meter"
					aria-label={LABELS[reading]}
					aria-valuenow={Math.round(value)}
					aria-valuemin="0"
					aria-valuemax="100"
				>
					<div class="stat-fill" style="width: {value}%; background: {tone(value)}"></div>
				</div>
			</div>
		{/each}
	{/if}
</div>

<style>
	.system-stats {
		display: flex;
		flex-direction: column;
		justify-content: center;
		gap: var(--space-md);
		height: 100%;
	}

	.stat {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.stat-header {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
	}

	.stat-label {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.stat-value {
		font-family: var(--font-sans);
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text);
		font-variant-numeric: tabular-nums;
	}

	.stat-bar {
		height: 5px;
		background: var(--color-bg-subtle);
		border-radius: 3px;
		overflow: hidden;
	}

	.stat-fill {
		height: 100%;
		border-radius: 3px;
		transition: width 0.4s ease;
	}

	.notice {
		margin: 0;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.notice.error {
		color: var(--color-error);
	}
</style>
