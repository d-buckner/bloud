<script lang="ts">
	import Icon from './Icon.svelte';

	interface Props {
		icon: string;
		onclick: (e: MouseEvent) => void;
		title?: string;
		size?: number;
		variant?: 'default' | 'ghost' | 'danger';
		label?: string;
		disabled?: boolean;
	}

	let {
		icon,
		onclick,
		title,
		size = 18,
		variant = 'default',
		label,
		disabled = false
	}: Props = $props();
</script>

<button
	class="icon-btn {variant}"
	class:with-label={label}
	{onclick}
	{title}
	{disabled}
	aria-label={title}
>
	<Icon name={icon} {size} />
	{#if label}
		<span>{label}</span>
	{/if}
</button>

<style>
	.icon-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-sm);
		padding: var(--space-sm);
		background: transparent;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.icon-btn.with-label {
		padding: var(--space-sm) var(--space-md);
	}

	.icon-btn span {
		font-family: var(--font-serif);
		font-size: 0.9375rem;
	}

	.icon-btn:hover:not(:disabled) {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.icon-btn:focus {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.icon-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	/* Ghost variant - no border, subtle */
	.icon-btn.ghost {
		border-color: transparent;
		color: var(--color-text-muted);
	}

	.icon-btn.ghost:hover:not(:disabled) {
		background: var(--color-bg-subtle);
		border-color: var(--color-border);
		color: var(--color-text);
	}

	/* Danger variant */
	.icon-btn.danger:hover:not(:disabled) {
		color: var(--color-error);
		border-color: var(--color-error);
	}
</style>
