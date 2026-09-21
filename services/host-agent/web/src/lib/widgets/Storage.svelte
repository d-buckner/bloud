<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { clampPercent, formatBytes, formatPercent } from '$lib/utils/format';

	interface StorageStats {
		used: number;
		total: number;
		free: number;
		percentage: number;
		path: string;
	}

	const REFRESH_MS = 30_000;

	let storage = $state<StorageStats | null>(null);
	let failed = $state(false);

	async function refresh() {
		try {
			const response = await fetch('/api/system/storage');
			if (!response.ok) throw new Error('request failed');
			storage = await response.json();
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

	/** Capacity is usually fine; only high usage is worth colour. */
	function tone(value: number): string {
		return value >= 90 ? 'var(--color-error)' : 'var(--color-accent)';
	}

	let used = $derived(storage ? clampPercent(storage.percentage) : 0);
</script>

<div class="storage">
	{#if failed}
		<p class="notice error">Storage metrics unavailable</p>
	{:else if !storage}
		<p class="notice">Reading…</p>
	{:else}
		<div class="headline">
			<span class="used">{formatBytes(storage.used)}</span>
			<span class="total">of {formatBytes(storage.total)}</span>
		</div>
		<div
			class="bar"
			role="meter"
			aria-label="Disk usage"
			aria-valuenow={Math.round(used)}
			aria-valuemin="0"
			aria-valuemax="100"
		>
			<div class="fill" style="width: {used}%; background: {tone(used)}"></div>
		</div>
		<div class="details">
			<span>{formatPercent(used)} used</span>
			<span>{formatBytes(storage.free)} free</span>
		</div>
		{#if storage.path}
			<span class="path" title={storage.path}>{storage.path}</span>
		{/if}
	{/if}
</div>

<style>
	.storage {
		display: flex;
		flex-direction: column;
		justify-content: center;
		gap: var(--space-sm);
		height: 100%;
	}

	.headline {
		display: flex;
		align-items: baseline;
		gap: var(--space-sm);
	}

	.used {
		font-family: var(--font-sans);
		font-size: 1.25rem;
		font-weight: 600;
		color: var(--color-text);
		font-variant-numeric: tabular-nums;
	}

	.total {
		font-size: 0.875rem;
		color: var(--color-text-muted);
	}

	.bar {
		height: 8px;
		background: var(--color-bg-subtle);
		border-radius: 4px;
		overflow: hidden;
	}

	.fill {
		height: 100%;
		border-radius: 4px;
		transition: width 0.4s ease;
	}

	.details {
		display: flex;
		justify-content: space-between;
		font-family: var(--font-sans);
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		font-variant-numeric: tabular-nums;
	}

	.path {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
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
