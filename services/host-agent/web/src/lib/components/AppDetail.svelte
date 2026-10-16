<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import AppIcon from './AppIcon.svelte';
	import CloseButton from './CloseButton.svelte';
	import IconButton from './IconButton.svelte';
	import Icon from './Icon.svelte';
	import type { CatalogApp } from '$lib/types';
	import type { BriefSection } from '$lib/utils/installBrief';
	import { isAdmin, select } from '$lib/store';

	const admin = select(isAdmin);

	/**
	 * The app detail, without a modal of its own.
	 *
	 * The catalog is a modal with two views, and this is the second one: the
	 * list swaps to a single app's page in the same dialog, and Back swaps back.
	 * The body was the inside of `AppDetailModal`, which owned its own dialog;
	 * a nested dialog for the detail would put one modal over another and make
	 * the back button the only way to see the list again.
	 */
	interface Props {
		app: CatalogApp;
		status?: string | null;
		/**
		 * What the install will do, built by the catalog from this app's metadata.
		 * The decision to install a multi-gigabyte app deserves more than one line
		 * of description: this is where the download size, the sign-in wiring, and
		 * any app pulled in alongside get stated, before the install rather than in
		 * the changelog after it.
		 */
		sections?: BriefSection[];
		/** Return to the catalog list. */
		onback: () => void;
		/** Close the whole catalog. */
		onclose: () => void;
		oninstall: (appName: string) => void;
	}

	let { app, status = null, sections = [], onback, onclose, oninstall }: Props = $props();

	let installed = $derived(status === 'running' || status === 'error' || status === 'failed');
	let installing = $derived(status === 'installing' || status === 'starting');
	let isUninstalling = $derived(status === 'uninstalling');
	// Why this app cannot be installed yet, straight from the API. The same rule
	// the orchestrator applies to the install, so the detail cannot offer a button
	// the backend would refuse.
	let blockers = $derived(app.installBlockers ?? []);
	let blocked = $derived(!installed && !installing && blockers.length > 0);

	let uninstallInProgress = $state(false);
	let uninstallError = $state<string | null>(null);

	function resetState() {
		uninstallInProgress = false;
		uninstallError = null;
	}

	async function doUninstall() {
		uninstallInProgress = true;
		uninstallError = null;

		try {
			const res = await fetch(`/api/apps/${app.catalogId}/uninstall`, { method: 'POST' });

			if (res.ok) {
				handleBack();
			} else {
				const result = await res.json();
				uninstallError = result.error || 'Uninstall failed';
			}
		} catch (err) {
			uninstallError = err instanceof Error ? err.message : 'Uninstall failed';
		} finally {
			uninstallInProgress = false;
		}
	}

	function doInstall() {
		oninstall(app.catalogId);
		handleBack();
	}

	function handleBack() {
		resetState();
		onback();
	}

	function formatAppName(name: string): string {
		return name.charAt(0).toUpperCase() + name.slice(1);
	}
</script>

<header class="detail-header">
	<IconButton
		icon="arrow-left"
		size={20}
		title="Back to catalog"
		variant="ghost"
		onclick={handleBack}
	/>
	<div class="detail-app-header">
		<AppIcon appName={app.catalogId} displayName={app.displayName} size="lg" />
		<div class="detail-app-info">
			<h2 id="app-detail-title">{app.displayName || formatAppName(app.catalogId)}</h2>
			{#if app.category}
				<span class="detail-app-category">{app.category}</span>
			{/if}
		</div>
	</div>
	<CloseButton onclick={onclose} />
</header>

<div class="detail-body">
	{#if app.description}
		<p class="detail-description">{app.description}</p>
	{/if}

	{#if app.screenshots?.length}
		<div class="screenshots">
			{#each app.screenshots as screenshot, i (i)}
				<img src={screenshot} alt="Screenshot" class="screenshot" />
			{/each}
		</div>
	{/if}

	{#if sections.length}
		<!-- Stated as a list of plain facts rather than as prose: the reader is
		     deciding, so each line has to be scannable in one pass. -->
		<div class="brief">
			{#each sections as section (section.title)}
				<div class="brief-section">
					<h3 class="brief-title">{section.title}</h3>
					<ul class="brief-list">
						{#each section.lines as line (line)}
							<li>{line}</li>
						{/each}
					</ul>
				</div>
			{/each}
		</div>
	{/if}

	{#if blocked}
		<div class="alert alert-error">
			<ul class="brief-list">
				{#each blockers as reason (reason)}
					<li>{reason}</li>
				{/each}
			</ul>
		</div>
	{/if}

	{#if installed}
		{#if uninstallError}
			<div class="alert alert-error">
				<p>{uninstallError}</p>
			</div>
		{/if}

		<div class="installed-notice">
			<Icon name="check-circle" size={20} />
			<span>This app is installed</span>
		</div>
	{/if}
</div>

<footer class="detail-footer">
	{#if isUninstalling}
		<button class="btn btn-secondary" onclick={handleBack}>Close</button>
		<span class="status-text">Uninstalling...</span>
	{:else if installed}
		<button class="btn btn-secondary" onclick={handleBack} disabled={uninstallInProgress}>Close</button>
		{#if admin.current}
			<button class="btn btn-danger" onclick={doUninstall} disabled={uninstallInProgress}>
				{#if uninstallInProgress}Removing...{:else}Uninstall{/if}
			</button>
		{/if}
	{:else}
		<button class="btn btn-secondary" onclick={handleBack}>Cancel</button>
		{#if admin.current}
			<!-- Disabled rather than hidden: the requirement is stated next to the
			     control it blocks, so the operator is not left hunting for why an
			     Install button is missing. -->
			<button
				class="btn btn-primary"
				onclick={doInstall}
				disabled={installing || blocked}
				title={blocked ? (app.installBlockers?.[0] ?? '') : ''}
			>
				{#if installing}Installing...{:else}Install{/if}
			</button>
		{/if}
	{/if}
</footer>

<style>
	.detail-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-md) var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}

	.detail-app-header {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		flex: 1;
		min-width: 0;
	}

	.detail-app-info h2 {
		margin: 0;
		font-size: 1.25rem;
		font-weight: 500;
	}

	.detail-app-category {
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		color: var(--color-text-muted);
	}

	.detail-body {
		padding: var(--space-lg);
	}

	.detail-footer {
		display: flex;
		gap: var(--space-sm);
		justify-content: flex-end;
		padding: var(--space-lg);
		border-top: 1px solid var(--color-border);
	}

	.detail-description {
		margin: 0 0 var(--space-lg) 0;
		font-size: 0.9375rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
	}

	/* The brief sits in its own panel so it reads as what Bloud will do, not as
	   more marketing copy from the app's own description. */
	.brief {
		display: grid;
		gap: var(--space-md);
		margin: 0 0 var(--space-lg) 0;
		padding: var(--space-md);
		background: var(--color-bg-subtle);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
	}

	.brief-section {
		display: grid;
		gap: var(--space-xs);
	}

	.brief-title {
		margin: 0;
		font-size: 0.6875rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
	}

	.brief-list {
		margin: 0;
		padding-left: var(--space-md);
		font-size: 0.875rem;
		line-height: 1.5;
		color: var(--color-text-secondary);
	}

	.brief-list li + li {
		margin-top: 2px;
	}

	.screenshots {
		display: flex;
		gap: var(--space-sm);
		overflow-x: auto;
		padding-bottom: var(--space-sm);
		margin-bottom: var(--space-lg);
	}

	.screenshot {
		flex-shrink: 0;
		width: 280px;
		height: auto;
		border-radius: var(--radius-md);
		border: 1px solid var(--color-border);
	}

	.installed-notice {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-md);
		background: var(--color-success-bg);
		color: var(--color-success);
		border-radius: var(--radius-md);
	}

	.alert {
		padding: var(--space-md);
		border-radius: var(--radius-md);
		margin-bottom: var(--space-md);
		font-size: 0.9375rem;
	}

	.alert-error {
		background: var(--color-error-bg);
		color: var(--color-error);
	}

	.btn {
		display: inline-flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-lg);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
		font-family: var(--font-serif);
		cursor: pointer;
		border: 1px solid transparent;
		transition: all 0.15s ease;
	}

	.btn-primary {
		background: var(--color-accent);
		color: var(--color-on-accent);
	}

	.btn-primary:hover:not(:disabled) {
		background: var(--color-accent-hover);
	}

	.btn-primary:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.btn-secondary {
		background: var(--color-bg-elevated);
		color: var(--color-text);
		border-color: var(--color-border);
	}

	.btn-secondary:hover:not(:disabled) {
		background: var(--color-bg-subtle);
	}

	.btn-danger {
		background: var(--color-danger);
		color: var(--color-on-danger);
	}

	.btn-danger:hover:not(:disabled) {
		background: var(--color-danger-hover);
	}

	.btn-danger:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.status-text {
		font-size: 0.9375rem;
		color: var(--color-text-muted);
		font-style: italic;
	}
</style>
