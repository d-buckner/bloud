<script lang="ts">
	import { onMount } from 'svelte';
	import type { Generation, GenerationsResponse } from '$lib/types';

	let generations: Generation[] = [];
	let loading = true;
	let error = '';

	onMount(async () => {
		try {
			const res = await fetch('/api/system/versions');
			const data: GenerationsResponse = await res.json();
			generations = data.generations || [];
			loading = false;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load history';
			loading = false;
		}
	});

	function timeAgo(dateStr: string): string {
		try {
			const parts = dateStr.split(' ');
			if (parts.length !== 2) return '';

			const date = new Date(parts[0] + 'T' + parts[1]);
			const now = new Date();
			const seconds = Math.floor((now.getTime() - date.getTime()) / 1000);

			if (seconds < 60) return 'just now';
			const minutes = Math.floor(seconds / 60);
			if (minutes < 60) return `${minutes} min ago`;
			const hours = Math.floor(minutes / 60);
			if (hours < 24) return `${hours} hour${hours > 1 ? 's' : ''} ago`;
			const days = Math.floor(hours / 24);
			if (days === 1) return 'yesterday';
			if (days < 7) return `${days} days ago`;
			const weeks = Math.floor(days / 7);
			if (weeks < 4) return `${weeks} week${weeks > 1 ? 's' : ''} ago`;
			const months = Math.floor(days / 30);
			return `${months} month${months > 1 ? 's' : ''} ago`;
		} catch {
			return '';
		}
	}

	function formatDate(dateStr: string): string {
		try {
			const parts = dateStr.split(' ');
			if (parts.length === 2) {
				const date = new Date(parts[0] + 'T' + parts[1]);
				return date.toLocaleDateString('en-US', {
					month: 'short',
					day: 'numeric',
					hour: 'numeric',
					minute: '2-digit'
				});
			}
			return dateStr;
		} catch {
			return dateStr;
		}
	}

	// Generate a human-readable description for a change
	// In the future, this would come from the API based on actual changes
	function getDescription(gen: Generation, index: number, total: number): string {
		if (gen.description) {
			return gen.description;
		}
		// Fallback descriptions based on position (data is sorted desc, so last item is oldest)
		if (index === total - 1) {
			return 'Initial system setup';
		}
		return 'System configuration updated';
	}
</script>

<svelte:head>
	<title>History · Bloud</title>
</svelte:head>

<div class="page">
	<header class="page-header">
		<h1>History</h1>
	</header>

	{#if loading}
		<div class="loading-state">
			<p>Loading...</p>
		</div>
	{:else if error}
		<div class="error-state">
			<p>{error}</p>
		</div>
	{:else if generations.length === 0}
		<div class="empty-state">
			<p>No changes recorded yet.</p>
		</div>
	{:else}
		<div class="timeline">
			{#each generations as gen, i (gen.number)}
				<div class="timeline-item" class:current={gen.current}>
					<div class="timeline-marker">
						{#if gen.current}
							<div class="marker-dot current"></div>
						{:else}
							<div class="marker-dot"></div>
						{/if}
						{#if i < generations.length - 1}
							<div class="marker-line"></div>
						{/if}
					</div>
					<div class="timeline-content">
						<div class="item-header">
							<span class="item-description">{getDescription(gen, i, generations.length)}</span>
							{#if gen.current}
								<span class="current-badge">Current</span>
							{/if}
						</div>
						<div class="item-meta">
							<span class="item-time" title={formatDate(gen.date)}>{timeAgo(gen.date)}</span>
						</div>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.page {
		padding: var(--space-2xl) var(--space-xl);
		max-width: 700px;
	}

	.page-header {
		margin-bottom: var(--space-xl);
	}

	.page-header h1 {
		margin: 0;
		font-size: 1.5rem;
		font-weight: 500;
	}

	.loading-state, .error-state, .empty-state {
		padding: var(--space-2xl);
		text-align: center;
		color: var(--color-text-muted);
	}

	.error-state {
		color: var(--color-error);
	}

	/* Timeline */
	.timeline {
		display: flex;
		flex-direction: column;
	}

	.timeline-item {
		display: flex;
		gap: var(--space-lg);
	}

	.timeline-marker {
		display: flex;
		flex-direction: column;
		align-items: center;
		flex-shrink: 0;
		width: 20px;
	}

	.marker-dot {
		width: 10px;
		height: 10px;
		border-radius: 50%;
		background: var(--color-border);
		border: 2px solid var(--color-bg);
		box-shadow: 0 0 0 2px var(--color-border);
		flex-shrink: 0;
	}

	.marker-dot.current {
		background: var(--color-success);
		box-shadow: 0 0 0 2px var(--color-success);
	}

	.marker-line {
		width: 2px;
		flex: 1;
		background: var(--color-border);
		margin: 4px 0;
	}

	.timeline-content {
		flex: 1;
		padding-bottom: var(--space-xl);
	}

	.timeline-item:last-child .timeline-content {
		padding-bottom: 0;
	}

	.item-header {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-bottom: 4px;
	}

	.item-description {
		font-weight: 500;
	}

	.current-badge {
		font-size: 0.6875rem;
		padding: 2px 6px;
		background: var(--color-success-bg);
		color: var(--color-success);
		border-radius: 4px;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		font-weight: 500;
	}

	.item-meta {
		margin-bottom: var(--space-sm);
	}

	.item-time {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}
</style>
