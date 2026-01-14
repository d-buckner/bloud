<script lang="ts">
	/**
	 * App Route Handler
	 *
	 * This page component handles routing only:
	 * - Opens the app tab
	 * - Restores to saved path when switching back to an open app
	 * - Updates the path in the tabs store
	 * - Shows error for non-existent apps
	 *
	 * The actual iframe rendering is handled by AppFrames in the layout.
	 */

	import { goto } from '$app/navigation';
	import { tabs } from '$lib/stores/tabs';
	import { apps } from '$lib/stores/apps';
	import { getAppRouteUrl } from '$lib/utils/embedUrl';
	import Icon from '$lib/components/Icon.svelte';

	let { data } = $props();

	let appName = $derived(data.appName);
	let path = $derived(data.path);

	// Check if app exists in the store
	let appExists = $derived($apps.some((a) => a.name === appName));
	let app = $derived($apps.find((a) => a.name === appName));

	// Handle tab opening and path restoration
	$effect(() => {
		if (!appName || !appExists) return;

		const isAlreadyOpen = tabs.isOpen(appName);
		const storedPath = tabs.getPath(appName);

		if (isAlreadyOpen && !path && storedPath) {
			// Switching back to open app without path - restore to stored path
			goto(getAppRouteUrl(appName, storedPath), { replaceState: true });
		} else {
			// New tab or explicit path navigation
			tabs.open(appName, path);
		}
	});
</script>

<svelte:head>
	<title>{app?.display_name ?? appName ?? 'App'} - Bloud</title>
</svelte:head>

<!-- Only show error state - iframe is rendered by AppFrames in layout -->
{#if appName && !appExists && $apps.length > 0}
	<div class="error-container">
		<div class="status-icon">
			<Icon name="warning" size={32} />
		</div>
		<p>App "{appName}" is not installed</p>
		<a href="/" class="back-link">Back to Apps</a>
	</div>
{/if}

<style>
	.error-container {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		height: 100vh;
		gap: var(--space-sm);
		color: var(--color-text-muted);
	}

	.error-container p {
		margin: 0;
		font-size: 1.125rem;
		color: var(--color-error);
	}

	.status-icon {
		margin-bottom: var(--space-sm);
		color: var(--color-error);
	}

	.back-link {
		margin-top: var(--space-md);
		color: var(--color-accent);
		text-decoration: none;
	}

	.back-link:hover {
		text-decoration: underline;
	}
</style>
