<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
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

	/* A disabled control has to look switched off, not switched to a different
	   colour. The old treatment was `opacity: 0.6` over the near-black fill, and
	   near-black at 60% over the cream canvas composites to #757370: a mid-grey
	   button, which is precisely what an "enabled but grey" button would look
	   like. The audit found Save and Test connection indistinguishable for that
	   reason, and a user who cannot tell whether a control is live does not press
	   it either way.

	   So disabled is drawn as the absence of a fill: the subtle background, the
	   muted label, a hairline border. It is the one button state that is not a
	   filled rectangle at all. The native `disabled` attribute carries the state
	   to assistive tech; nothing here needs aria-disabled on top of it. */
	.btn:disabled {
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
		border-color: var(--color-border);
		box-shadow: none;
		cursor: not-allowed;
		opacity: 1;
	}

	.btn.ghost:disabled,
	.btn.danger:disabled {
		background: var(--color-bg-subtle);
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
