<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
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
	aria-label={title || label}
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
		/* A glyph alone is a 16px target wearing a button. The hit area is the
		   padding around it, and the padding has to be told. */
		min-width: var(--tap-target-min);
		min-height: var(--tap-target-min);
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

	/* --color-focus, not --color-accent: the accent is the near-black the buttons
	   themselves are filled with, so an accent ring is 1.00:1 against its own
	   control. :focus-visible, so a click does not leave the ring behind. */
	.icon-btn:focus-visible {
		outline: 2px solid var(--color-focus);
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
