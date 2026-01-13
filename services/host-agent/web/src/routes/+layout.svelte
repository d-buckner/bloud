<script lang="ts">
	import '../app.css';
	import type { Snippet } from 'svelte';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import { initApps, disconnectApps } from '$lib/services/appLifecycle';
	import { waitForServiceWorker, setActiveApp } from '$lib/services/bootstrap';

	let { children }: { children: Snippet } = $props();

	let sidebarCollapsed = $state(false);

	let isAppView = $derived(page.url.pathname.startsWith('/apps/'));

	// Clear active app when navigating away from app pages
	// The app page component sets the active app when it loads
	$effect(() => {
		if (!isAppView) {
			setActiveApp(null);
		}
	});

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

	<main class:collapsed={sidebarCollapsed || isAppView}>
		{@render children()}
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
	}

	main.collapsed {
		margin-left: 64px;
	}

	@media (max-width: 768px) {
		main {
			margin-left: 64px;
		}
	}
</style>
