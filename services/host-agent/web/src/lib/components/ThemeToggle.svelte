<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Icon from './Icon.svelte';
	import { themeLabel } from '$lib/theme';
	import { theme } from '$lib/theme.svelte';

	interface Props {
		/** True when the sidebar hides its text labels. */
		collapsed?: boolean;
	}

	let { collapsed = false }: Props = $props();

	// The control names the theme it is about to give you, not the one you are
	// already looking at. A button that shows the sun while the page is light
	// reads as a state indicator, and pressing it appears to do nothing.
	const target = $derived(theme.resolved === 'dark' ? 'light' : 'dark');
	const label = $derived(themeLabel(target));
</script>

<button
	type="button"
	class="theme-toggle"
	title={label}
	aria-label={label}
	onclick={() => theme.toggle()}
>
	<Icon name={target === 'dark' ? 'moon' : 'sun'} size={16} />
	{#if !collapsed}
		<span class="theme-label">{target === 'dark' ? 'Dark' : 'Light'}</span>
	{/if}
</button>

<style>
	.theme-toggle {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		padding: var(--space-xs) var(--space-sm);
		background: transparent;
		border: none;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		font-family: var(--font-sans);
		font-size: 0.8125rem;
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.theme-toggle:hover {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.theme-label {
		white-space: nowrap;
	}
</style>
