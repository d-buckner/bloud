<script lang="ts">
	import '../app.css';
	import type { Snippet } from 'svelte';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import AppFrames from '$lib/components/AppFrames.svelte';
	import { initApps, disconnectApps } from '$lib/services/appLifecycle';
	import { waitForServiceWorker } from '$lib/services/bootstrap';

	let { children }: { children: Snippet } = $props();

	let sidebarCollapsed = $state(false);

	let isAppView = $derived(page.url.pathname.startsWith('/apps/'));

	// Initialize app store SSE connection and service worker
	onMount(() => {
		initApps();
		// Register service worker for URL rewriting in embedded apps
		waitForServiceWorker();
		return () => disconnectApps();
	});
</script>

<div class="app">
	<Sidebar bind:collapsed={sidebarCollapsed} />

	<main class:collapsed={sidebarCollapsed}>
		<!-- AppFrames manages all open iframes, preserving state across tab switches -->
		<AppFrames visible={isAppView} />

		<!-- Regular route content (hidden when viewing apps) -->
		<div class="route-content" class:hidden={isAppView}>
			{@render children()}
		</div>
	</main>
</div>

<style>
	.app {
		display: flex;
		min-height: 100vh;
	}

	main {
		flex: 1;
		margin-left: var(--sidebar-width);
		min-height: 100vh;
		transition: margin-left 0.2s ease;
		display: flex;
		flex-direction: column;
	}

	main.collapsed {
		margin-left: 64px;
	}

	.route-content {
		flex: 1;
	}

	.route-content.hidden {
		display: none;
	}

	@media (max-width: 768px) {
		main {
			margin-left: 64px;
		}
	}
</style>
