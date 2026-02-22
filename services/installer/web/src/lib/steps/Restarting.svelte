<script lang="ts">
	import { onMount, onDestroy } from 'svelte';

	let slow = $state(false);
	let interval: ReturnType<typeof setInterval> | null = null;
	let slowTimeout: ReturnType<typeof setTimeout> | null = null;

	onMount(() => {
		slowTimeout = setTimeout(() => {
			slow = true;
		}, 5 * 60 * 1000);

		interval = setInterval(async () => {
			try {
				const res = await fetch('/api/health');
				if (res.ok) {
					window.location.href = '/';
				}
			} catch {
				// Machine is rebooting — network errors are expected, keep polling
			}
		}, 3000);
	});

	onDestroy(() => {
		if (interval) clearInterval(interval);
		if (slowTimeout) clearTimeout(slowTimeout);
	});
</script>

<div class="card">
	<span class="wordmark">bloud</span>

	<div class="body">
		<div class="spinner-wrap" aria-label="Restarting" role="status">
			<div class="ring"></div>
		</div>

		<div class="text">
			<h2 class="headline">Your server is restarting.</h2>
			<p class="caption">You'll be redirected automatically when it's ready.</p>
		</div>
	</div>

	{#if slow}
		<p class="slow-note">Taking longer than expected — check that the machine is on.</p>
	{/if}
</div>

<style>
	.card {
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		padding: var(--space-2xl);
		width: 100%;
		max-width: 420px;
		box-shadow: var(--shadow-md);
		display: flex;
		flex-direction: column;
		gap: var(--space-xl);
	}

	.wordmark {
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		font-weight: 400;
		letter-spacing: 0.08em;
		color: var(--color-text-secondary);
		text-transform: lowercase;
	}

	.body {
		display: flex;
		flex-direction: column;
		gap: var(--space-xl);
	}

	.spinner-wrap {
		display: flex;
		align-items: center;
		justify-content: flex-start;
	}

	.ring {
		width: 2rem;
		height: 2rem;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 0.9s linear infinite;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	.text {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.headline {
		font-size: 1.375rem;
		font-weight: 400;
		color: var(--color-text);
		margin: 0;
		line-height: 1.3;
	}

	.caption {
		margin: 0;
		font-size: 0.9375rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
	}

	.slow-note {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		line-height: 1.5;
	}
</style>
