<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { browser } from '$app/environment';
	import AppIcon from './AppIcon.svelte';
	import Icon from './Icon.svelte';
	import type { CatalogApp } from '$lib/types';
	import { getAppUrl } from '$lib/utils/appUrl';
	import { catalogCardLabel, catalogCardState } from '$lib/utils/catalogCard';

	interface Props {
		app: CatalogApp;
		status?: string | null;
		onclick: () => void;
	}

	let { app, status = null, onclick }: Props = $props();

	let state = $derived(catalogCardState(status));
	let installed = $derived(state === 'installed');
	let installing = $derived(state === 'installing');
	let uninstalling = $derived(state === 'uninstalling');

	let displayName = $derived(app.displayName || formatAppName(app.catalogId));
	let sizeLabel = $derived(formatSizeMB(app.estimatedSizeMB));
	let cardLabel = $derived(catalogCardLabel(displayName, state));

	// An installed card opens the app as well as managing it, so the state
	// carries a consequence instead of being a colour.
	let openUrl = $derived(browser ? getAppUrl(app.catalogId) : '');

	function formatAppName(name: string): string {
		return name.charAt(0).toUpperCase() + name.slice(1);
	}

	function formatSizeMB(mb?: number): string {
		if (!mb || mb <= 0) return '';
		if (mb >= 1024) return `~${(mb / 1024).toFixed(1)} GB`;
		return `~${mb} MB`;
	}
</script>

<article
	class="app-card"
	class:installed
	class:installing
	aria-busy={installing || uninstalling}
>
	<div class="icon-wrapper">
		<AppIcon appName={app.catalogId} displayName={app.displayName} transparent={installing} />
		{#if installing}
			<div class="install-spinner" role="status" aria-label="Installing"></div>
		{/if}
	</div>
	<div class="app-content">
		<div class="app-header">
			<h3 class="app-title">
				<button class="card-action" {onclick} disabled={installing} aria-label={cardLabel}>
					{displayName}
				</button>
			</h3>
			{#if sizeLabel}
				<span class="app-size">{sizeLabel}</span>
			{/if}
			{#if app.category}
				<span class="app-category">{app.category}</span>
			{/if}
		</div>
		{#if app.description}
			<p class="app-description">{app.description}</p>
		{/if}
	</div>

	{#if installed || uninstalling}
		<div class="card-side">
			{#if uninstalling}
				<span class="state-badge neutral">Uninstalling</span>
			{:else if installed}
				<span class="state-badge">
					<Icon name="check-circle" size={12} />
					Installed
				</span>
				<!-- An app's own subdomain is not a Bloud route, so resolve() has nothing
				     to say about it. A link rather than a button calling window.open so
				     middle-click and "open in new tab" still work. -->
				<!-- eslint-disable svelte/no-navigation-without-resolve -->
				<a
					class="open-link"
					href={openUrl}
					target="_blank"
					rel="noopener"
					aria-label={`Open ${displayName} in a new tab`}
				>
					Open
					<Icon name="external-link" size={12} />
				</a>
				<!-- eslint-enable svelte/no-navigation-without-resolve -->
			{/if}
		</div>
	{/if}
</article>

<style>
	/* The card is an article, not a button: an installed card also carries an Open
	   link, and a link inside a button is not markup anything will honour. The
	   title is the control, and its ::after covers the card so the whole surface
	   is still clickable. That keeps the button's own visible text to the app
	   name, which is what lets the accessible name stay short without tripping
	   WCAG 2.5.3 (a control's visible label has to appear in its name). */
	.app-card {
		position: relative;
		display: flex;
		/* Wrapped rather than widened: the side column is real content, and a
		   nowrap card would set the minimum width of every route that shows one
		   (the catalog already overflows at 390px). Below the width where both
		   columns fit, the state gets its own line. */
		flex-wrap: wrap;
		align-items: flex-start;
		gap: var(--space-md);
		padding: var(--space-lg);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		transition: all 0.15s ease;
	}

	.app-card:hover {
		border-color: var(--color-text-muted);
		transform: translateY(-1px);
		box-shadow: var(--shadow-sm);
	}

	/* Installed keeps its border and the border carries the state. The tint it
	   used to replace the border with measured 1.04:1 against an uninstalled
	   card, which is no separation at all, and it disappeared entirely under
	   deuteranopia simulation. A solid --color-success border is 7:1 against
	   both the card and the page, so it survives greyscale. */
	.app-card.installed {
		border: 2px solid var(--color-success);
	}

	.app-card.installed:hover {
		border-color: var(--color-success);
	}

	.app-card:has(.card-action:focus-visible) {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.card-action {
		display: block;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		padding: 0;
		margin: 0;
		background: none;
		border: none;
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
	}

	/* The stretched hit area: the whole card is the control, the way it always
	   was, without the whole card becoming the button's accessible name. */
	.card-action::after {
		content: '';
		position: absolute;
		inset: 0;
		border-radius: var(--radius-lg);
	}

	.card-action:focus {
		outline: none;
	}

	.card-action:disabled {
		cursor: default;
	}

	.app-card:hover :global(.app-icon img) {
		filter: grayscale(0%);
		opacity: 1;
	}

	.app-card.installed :global(.app-icon img) {
		filter: grayscale(0%);
		opacity: 1;
	}

	.app-card :global(.app-icon img) {
		filter: grayscale(100%);
		opacity: 0.8;
		transition: filter 0.2s ease, opacity 0.2s ease;
	}

	.app-content {
		/* min-content as the basis is what makes the wrap decision honest: the
		   side column moves to its own line exactly when the card is too narrow
		   to hold both, not at a breakpoint guessed in viewport units. */
		flex: 1 1 min-content;
		min-width: 0;
		overflow: hidden;
	}

	.app-header {
		display: flex;
		align-items: center;
		/* The header is the widest thing on the card only because its three
		   pieces sit on one line: letting them wrap drops the card's minimum
		   width to a single chip, which is what keeps the card from setting the
		   minimum width of the page. */
		flex-wrap: wrap;
		gap: var(--space-sm);
	}

	.app-title {
		margin: 0;
		font-size: 1rem;
		font-weight: 500;
		white-space: nowrap;
		min-width: 0;
	}

	.app-size {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		white-space: nowrap;
		flex-shrink: 0;
	}

	.app-category {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		color: var(--color-text-muted);
		background: var(--color-bg-subtle);
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		flex-shrink: 0;
	}

	.app-description {
		margin: var(--space-xs) 0 0 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.4;
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}

	.card-side {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		justify-content: space-between;
		gap: var(--space-sm);
		flex-shrink: 0;
		align-self: stretch;
		/* When the card is too narrow for both columns the side column falls to
		   its own line, and this is what pulls it back to the right edge there.
		   On a line that has no free space to distribute it does nothing. */
		margin-left: auto;
	}

	/* A pill, not italic text: italic reads as decorative, which is the wrong
	   register for the one fact about the card that changes what it does. The
	   check icon means the state still reads with the colour taken out. */
	.state-badge {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 2px 8px;
		border-radius: 9999px;
		background: var(--color-success-tint);
		color: var(--color-success);
		font-size: 0.75rem;
		font-weight: 600;
		white-space: nowrap;
	}

	.state-badge.neutral {
		background: var(--color-bg-subtle);
		color: var(--color-text-secondary);
	}

	.open-link {
		position: relative;
		/* Above the title button's stretched hit area, or the Open action would
		   be unreachable wherever the card is also clickable. */
		z-index: 1;
		display: inline-flex;
		align-items: center;
		gap: 4px;
		margin-top: auto;
		padding: 4px 10px;
		border: 1px solid var(--color-success);
		border-radius: var(--radius-md);
		background: var(--color-bg-elevated);
		color: var(--color-success);
		font-size: 0.8125rem;
		font-weight: 500;
		text-decoration: none;
		white-space: nowrap;
		transition: background 0.15s ease, color 0.15s ease;
	}

	.open-link:hover {
		background: var(--color-success);
		color: #fff;
	}

	.icon-wrapper {
		position: relative;
		flex-shrink: 0;
		width: 44px;
		height: 44px;
	}

	.app-card.installing {
		opacity: 0.7;
		pointer-events: none;
	}

	.install-spinner {
		position: absolute;
		inset: -4px;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 1s linear infinite;
	}


	@keyframes spin {
		to { transform: rotate(360deg); }
	}
</style>
