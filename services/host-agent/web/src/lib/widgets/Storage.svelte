<script lang="ts">
	interface StorageStats {
		used: number;
		total: number;
		free: number;
		percentage: number;
		path: string;
	}

	let storage = $state<StorageStats | null>(null);
	let error = $state<string | null>(null);
	let loading = $state(true);

	function formatBytes(bytes: number): string {
		if (bytes === 0) return '0 B';
		const k = 1024;
		const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
		const i = Math.floor(Math.log(bytes) / Math.log(k));
		return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
	}

	async function fetchStorage() {
		try {
			const response = await fetch('/api/system/storage');
			if (!response.ok) {
				throw new Error('Failed to fetch storage stats');
			}
			storage = await response.json();
			error = null;
		} catch (e) {
			error = e instanceof Error ? e.message : 'Unknown error';
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		fetchStorage();
		const interval = setInterval(fetchStorage, 30000); // Refresh every 30 seconds
		return () => clearInterval(interval);
	});
</script>

<div class="storage-widget">
	{#if loading}
		<div class="loading">Loading...</div>
	{:else if error}
		<div class="error">{error}</div>
	{:else if storage}
		<div class="storage-info">
			<div class="storage-bar-container">
				<div class="storage-bar" style="width: {storage.percentage}%"></div>
			</div>
			<div class="storage-details">
				<span class="storage-used">{formatBytes(storage.used)} used</span>
				<span class="storage-total">of {formatBytes(storage.total)}</span>
			</div>
			<div class="storage-free">
				{formatBytes(storage.free)} free
			</div>
		</div>
	{/if}
</div>

<style>
	.storage-widget {
		min-height: 60px;
	}

	.loading,
	.error {
		color: var(--color-text-muted);
		font-size: 0.875rem;
		text-align: center;
		padding: var(--space-md);
	}

	.error {
		color: var(--color-error, #dc2626);
	}

	.storage-info {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.storage-bar-container {
		height: 8px;
		background: var(--color-bg-subtle);
		border-radius: 4px;
		overflow: hidden;
	}

	.storage-bar {
		height: 100%;
		background: var(--color-accent);
		border-radius: 4px;
		transition: width 0.3s ease;
	}

	.storage-details {
		display: flex;
		justify-content: space-between;
		font-size: 0.875rem;
	}

	.storage-used {
		font-weight: 500;
		color: var(--color-text);
	}

	.storage-total {
		color: var(--color-text-muted);
	}

	.storage-free {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}
</style>
