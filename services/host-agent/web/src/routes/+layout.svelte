<script lang="ts">
	import '../app.css';
	import type { Snippet } from 'svelte';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import AppFrames from '$lib/components/AppFrames.svelte';
	import SetupWizard from '$lib/components/SetupWizard.svelte';
	import { initApps, disconnectApps } from '$lib/services/appLifecycle';
	import { waitForServiceWorker } from '$lib/services/bootstrap';

	interface SetupStatus {
		setupRequired: boolean;
		authentikReady: boolean;
	}

	let { children }: { children: Snippet } = $props();

	let sidebarCollapsed = $state(false);
	let setupRequired = $state(false);
	let loading = $state(true);

	let isAppView = $derived(page.url.pathname.startsWith('/apps/'));

	// Check setup status first, then initialize app if setup complete
	onMount(() => {
		checkSetupStatus();

		return () => {
			disconnectApps();
		};
	});

	async function checkSetupStatus() {
		try {
			const res = await fetch('/api/setup/status');
			const data: SetupStatus = await res.json();
			setupRequired = data.setupRequired;
		} catch {
			// If we can't reach the API, proceed with normal app
			setupRequired = false;
		}
		loading = false;

		// Only initialize app connections if setup is complete
		if (!setupRequired) {
			initApps();
			waitForServiceWorker();
		}
	}
</script>

{#if loading}
	<div class="loading">
		<div class="spinner"></div>
	</div>
{:else if setupRequired}
	<SetupWizard />
{:else}
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
{/if}

<style>
	.loading {
		min-height: 100vh;
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--color-bg);
	}

	.spinner {
		width: 32px;
		height: 32px;
		border: 3px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

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
