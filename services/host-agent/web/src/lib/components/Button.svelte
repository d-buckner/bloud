<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		variant?: 'primary' | 'secondary' | 'danger' | 'ghost';
		size?: 'sm' | 'md';
		disabled?: boolean;
		type?: 'button' | 'submit';
		onclick?: () => void;
		children: Snippet;
	}

	let {
		variant = 'primary',
		size = 'md',
		disabled = false,
		type = 'button',
		onclick,
		children
	}: Props = $props();
</script>

<button
	class="btn"
	class:primary={variant === 'primary'}
	class:secondary={variant === 'secondary'}
	class:danger={variant === 'danger'}
	class:ghost={variant === 'ghost'}
	class:sm={size === 'sm'}
	{type}
	{disabled}
	{onclick}
>
	{@render children()}
</button>

<style>
	.btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-lg);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
		font-family: var(--font-serif);
		font-weight: 400;
		cursor: pointer;
		border: 1px solid transparent;
		transition: all 0.15s ease;
		white-space: nowrap;
	}

	.btn:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.btn.sm {
		padding: var(--space-xs) var(--space-md);
		font-size: 0.875rem;
	}

	/* Primary */
	.btn.primary {
		background: var(--color-accent);
		color: white;
	}

	.btn.primary:hover:not(:disabled) {
		background: var(--color-accent-hover);
	}

	/* Secondary */
	.btn.secondary {
		background: var(--color-bg-elevated);
		color: var(--color-text);
		border-color: var(--color-border);
	}

	.btn.secondary:hover:not(:disabled) {
		background: var(--color-bg-subtle);
	}

	/* Danger */
	.btn.danger {
		background: var(--color-error);
		color: white;
	}

	.btn.danger:hover:not(:disabled) {
		background: #7f1d1d;
	}

	/* Ghost */
	.btn.ghost {
		background: transparent;
		color: var(--color-text-secondary);
	}

	.btn.ghost:hover:not(:disabled) {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}
</style>
