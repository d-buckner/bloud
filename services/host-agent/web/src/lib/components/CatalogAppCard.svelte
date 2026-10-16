<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { browser } from '$app/environment';
	import AppIcon from './AppIcon.svelte';
	import Icon from './Icon.svelte';
	import type { CatalogApp } from '$lib/types';
	import { getAppUrl } from '$lib/utils/appUrl';
	import { formatSizeMB } from '$lib/utils/format';
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
	// A required endpoint the operator has not configured yet. The card says so
	// rather than offering an install the orchestrator will refuse, and the modal
	// spells out the same sentence the API returned.
	let blocked = $derived(!installed && !installing && (app.installBlockers?.length ?? 0) > 0);

	let displayName = $derived(app.displayName || formatAppName(app.catalogId));
	let sizeLabel = $derived(formatSizeMB(app.estimatedSizeMB));
	let cardLabel = $derived(catalogCardLabel(displayName, state));
	let hasMeta = $derived(Boolean(app.category) || Boolean(sizeLabel));

	// An installed card opens the app as well as managing it, so the state
	// carries a consequence instead of being a colour.
	let openUrl = $derived(browser ? getAppUrl(app.catalogId) : '');

	function formatAppName(name: string): string {
		return name.charAt(0).toUpperCase() + name.slice(1);
	}
</script>

<article
	class="app-card"
	class:installed
	class:installing
	aria-busy={installing || uninstalling}
>
	<!-- The icon and the state share the top row, so the state is in the same
	     place on every card and the body below it can align row to row. The old
	     card put the state in a side column that wrapped under the text at a
	     narrow width, which is what made the grid rows ragged. -->
	<div class="card-top">
		<div class="icon-wrapper">
			<AppIcon appName={app.catalogId} displayName={app.displayName} transparent={installing} />
			{#if installing}
				<div class="install-spinner" role="status" aria-label="Installing"></div>
			{/if}
		</div>
		{#if installed}
			<span class="state-badge">
				<Icon name="check-circle" size={12} />
				Installed
			</span>
		{:else if uninstalling}
			<span class="state-badge neutral">Uninstalling</span>
		{:else if blocked}
			<!-- title carries the first reason so the badge alone is not the only
			     place the requirement is stated. The modal states it in full. -->
			<span class="state-badge neutral" title={app.installBlockers?.[0]}>Needs setup</span>
		{/if}
	</div>

	<h3 class="app-title">
		<button class="card-action" {onclick} disabled={installing} aria-label={cardLabel}>
			{displayName}
		</button>
	</h3>

	{#if hasMeta}
		<p class="app-meta">
			{#if app.category}
				<span class="app-category">{app.category}</span>
			{/if}
			{#if app.category && sizeLabel}
				<span class="meta-sep" aria-hidden="true">·</span>
			{/if}
			{#if sizeLabel}
				<span class="app-size">{sizeLabel}</span>
			{/if}
		</p>
	{/if}

	{#if app.description}
		<p class="app-description">{app.description}</p>
	{/if}

	{#if installed}
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
</article>

<style>
	/* A vertical card: icon and state on top, then the name, the meta line, the
	   description, and the action. The card is an article, not a button: an
	   installed card also carries an Open link, and a link inside a button is not
	   markup anything will honour. The title is the control, and its ::after
	   covers the card so the whole surface is still clickable. That keeps the
	   button's own visible text to the app name, which is what lets the accessible
	   name stay short without tripping WCAG 2.5.3 (a control's visible label has
	   to appear in its name). */
	.app-card {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		/* One height for every card, so the grid is even whether an app carries a
		   state, a long name, or a third line of description. */
		min-height: 216px;
		padding: var(--space-md);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		transition: border-color 0.15s ease, box-shadow 0.15s ease, transform 0.15s ease;
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
	.app-card.installed,
	.app-card.installed:hover {
		border-color: var(--color-success);
	}

	.app-card:has(.card-action:focus-visible) {
		outline: 2px solid var(--color-focus);
		outline-offset: 2px;
	}

	.card-top {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: var(--space-sm);
	}

	.icon-wrapper {
		position: relative;
		flex-shrink: 0;
		width: 40px;
		height: 40px;
	}

	.card-action {
		display: block;
		max-width: 100%;
		padding: 0;
		margin: 0;
		background: none;
		border: none;
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
		/* Wraps instead of truncating. "AFFiNE" and "AFFiNE ..." are two apps that
		   install two different things, "Hermes" and "Hermes ..." likewise, and
		   "Cal" is everything that survived of Calino. A truncated product name is
		   not a shorter name, it is a different one. */
		white-space: normal;
		overflow-wrap: anywhere;
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

	.app-title {
		margin: 0;
		font-size: 0.9375rem;
		font-weight: 600;
	}

	.app-meta {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-xs) var(--space-sm);
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.app-category {
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	.meta-sep {
		color: var(--color-border-strong);
	}

	.app-description {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.45;
		/* Three lines, then a real ellipsis at a word boundary. The -webkit prefix
		   is still the only way to get the clamp and the glyph together; without it
		   the description was cut mid-word, which reads as a bug rather than as
		   there being more to read. */
		display: -webkit-box;
		-webkit-line-clamp: 3;
		line-clamp: 3;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}

	/* A pill, not italic text: italic reads as decorative, which is the wrong
	   register for the one fact about the card that changes what it does. */
	.state-badge {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		flex-shrink: 0;
		padding: 2px 8px;
		border-radius: 9999px;
		background: var(--color-success-tint);
		color: var(--color-success);
		font-size: 0.6875rem;
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
		/* Pushed to the bottom so every Open sits on the same line. */
		margin-top: auto;
		align-self: flex-start;
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
		color: var(--color-on-success);
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
