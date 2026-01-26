<script lang="ts">
	import Icon from './Icon.svelte';

	interface Props {
		icon?: string;
		title?: string;
		description?: string;
		actionLabel?: string;
		actionHref?: string;
	}

	let {
		icon = 'grid',
		title = 'No apps yet',
		description = 'Browse the App Store to get started',
		actionLabel = 'Get Apps',
		actionHref = '/catalog'
	}: Props = $props();
</script>

<div class="empty-state">
	<div class="empty-icon">
		<Icon name={icon} size={64} />
	</div>
	<h2>{title}</h2>
	<p>{description}</p>
	{#if actionLabel && actionHref}
		<a href={actionHref} class="action-btn">{actionLabel}</a>
	{/if}
</div>

<style>
	.empty-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		text-align: center;
		gap: var(--space-md);
		padding-top: var(--space-3xl);
	}

	.empty-icon {
		color: var(--color-text-muted);
		opacity: 0.5;
		margin-bottom: var(--space-sm);
	}

	.empty-state h2 {
		margin: 0;
		font-size: 1.25rem;
		font-weight: 500;
	}

	.empty-state p {
		margin: 0;
		color: var(--color-text-muted);
		font-size: 0.9375rem;
	}

	.action-btn {
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-xl);
		background: var(--color-accent);
		color: white;
		border: none;
		border-radius: var(--radius-md);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		font-weight: 500;
		text-decoration: none;
		cursor: pointer;
		transition: background 0.15s ease;
	}

	.action-btn:hover {
		background: var(--color-accent-hover);
	}
</style>
