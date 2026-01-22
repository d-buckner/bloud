<script lang="ts">
	import type { WidgetProps } from './registry';

	let { config }: WidgetProps = $props();

	interface Stats {
		cpu: number;
		memory: number;
		disk: number;
	}

	let stats = $state<Stats | null>(null);
	let error = $state<string | null>(null);

	async function fetchStats() {
		try {
			const response = await fetch('/api/system/status');
			if (!response.ok) throw new Error('Failed to fetch');
			stats = await response.json();
			error = null;
		} catch (e) {
			error = 'Unable to load';
		}
	}

	$effect(() => {
		fetchStats();
		const interval = setInterval(fetchStats, 10000); // Refresh every 10s
		return () => clearInterval(interval);
	});

	function getStatusColor(value: number): string {
		if (value >= 90) return 'var(--color-error)';
		if (value >= 75) return 'var(--color-warning)';
		return 'var(--color-success)';
	}
</script>

<div class="system-stats">
	{#if error}
		<p class="error">{error}</p>
	{:else if !stats}
		<div class="loading">Loading...</div>
	{:else}
		<div class="stat-row">
			<div class="stat">
				<div class="stat-header">
					<span class="stat-label">CPU</span>
					<span class="stat-value">{Math.round(stats.cpu)}%</span>
				</div>
				<div class="stat-bar">
					<div
						class="stat-fill"
						style="width: {stats.cpu}%; background: {getStatusColor(stats.cpu)}"
					></div>
				</div>
			</div>

			<div class="stat">
				<div class="stat-header">
					<span class="stat-label">Memory</span>
					<span class="stat-value">{Math.round(stats.memory)}%</span>
				</div>
				<div class="stat-bar">
					<div
						class="stat-fill"
						style="width: {stats.memory}%; background: {getStatusColor(stats.memory)}"
					></div>
				</div>
			</div>

			<div class="stat">
				<div class="stat-header">
					<span class="stat-label">Disk</span>
					<span class="stat-value">{Math.round(stats.disk)}%</span>
				</div>
				<div class="stat-bar">
					<div
						class="stat-fill"
						style="width: {stats.disk}%; background: {getStatusColor(stats.disk)}"
					></div>
				</div>
			</div>
		</div>
	{/if}
</div>

<style>
	.system-stats {
		display: flex;
		flex-direction: column;
		height: 100%;
		justify-content: center;
	}

	.stat-row {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.stat {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.stat-header {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
	}

	.stat-label {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		font-weight: 500;
	}

	.stat-value {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text);
		font-variant-numeric: tabular-nums;
	}

	.stat-bar {
		height: 6px;
		background: var(--color-border);
		border-radius: 3px;
		overflow: hidden;
	}

	.stat-fill {
		height: 100%;
		border-radius: 3px;
		transition: width 0.3s ease;
	}

	.loading {
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.error {
		color: var(--color-error);
		font-size: 0.875rem;
		margin: 0;
	}
</style>
