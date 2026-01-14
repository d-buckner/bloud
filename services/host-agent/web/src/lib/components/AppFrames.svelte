<script lang="ts">
	/**
	 * AppFrames - Container for all open app iframes
	 *
	 * Manages multiple AppFrame instances, keeping them alive when switching
	 * between tabs instead of destroying them. Handles:
	 * - SW active app communication
	 * - URL sync with browser
	 * - Show/hide based on active tab
	 */

	import { onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { openAppNames, activeAppName, appPaths } from '$lib/stores/tabs';
	import { apps } from '$lib/stores/apps';
	import { setActiveApp } from '$lib/services/bootstrap';
	import { waitForServiceWorker } from '$lib/services/bootstrap';
	import AppFrame from './AppFrame.svelte';
	import type { App } from '$lib/types';

	interface Props {
		visible: boolean;
	}

	let { visible }: Props = $props();

	// Get app objects for open tabs
	const openApps = $derived.by(() => {
		const appMap = new Map($apps.map((a) => [a.name, a]));
		return $openAppNames
			.map((name) => appMap.get(name))
			.filter((a): a is App => a !== undefined);
	});

	// Track SW ready state
	let swReady = $state(false);

	// Wait for SW on mount
	waitForServiceWorker().then(() => {
		swReady = true;
	});

	// Update SW active app when active tab changes
	$effect(() => {
		if (!swReady) return;
		const active = $activeAppName;

		if (visible && active) {
			setActiveApp(active);
		} else if (!visible) {
			setActiveApp(null);
		}
	});

	// Clear active app on destroy
	onDestroy(() => {
		setActiveApp(null);
	});

	function handleNavigate(appName: string, path: string) {
		// Update browser URL when iframe navigates
		const newUrl = `/apps/${appName}/${path}`;
		const currentUrl = window.location.pathname + window.location.search;

		if (currentUrl !== newUrl) {
			goto(newUrl, { replaceState: true, noScroll: true });
		}
	}
</script>

<div class="app-frames" class:hidden={!visible}>
	{#if swReady}
		{#each openApps as app (app.name)}
			<AppFrame
				{app}
				path={$appPaths[app.name] ?? ''}
				isActive={visible && app.name === $activeAppName}
				onNavigate={(path) => handleNavigate(app.name, path)}
			/>
		{/each}
	{/if}
</div>

<style>
	.app-frames {
		flex: 1;
		position: relative;
		min-height: 0; /* Allow flex child to shrink */
	}

	.app-frames.hidden {
		display: none;
	}
</style>
