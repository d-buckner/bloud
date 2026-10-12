<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import '../app.css';
	import type { Snippet } from 'svelte';
	import { onMount } from 'svelte';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import SetupWizard from '$lib/components/SetupWizard.svelte';
	import Toasts from '$lib/components/Toasts.svelte';
	import { select, startDashboard, stopTransport } from '$lib/store';

	let { children }: { children: Snippet } = $props();

	let sidebarCollapsed = $state(false);

	// The session read and the stream are the store's business now. This page only
	// says when the dashboard starts and stops, and renders the four states the
	// session slice can be in: not asked yet, anonymous, needs setup, ready.
	const session = select((s) => s.session);

	onMount(() => {
		startDashboard();
		return () => stopTransport();
	});
</script>

{#if session.current.status === 'bootstrapping'}
	<div class="loading">
		<div class="spinner"></div>
	</div>
{:else if session.current.setupRequired}
	<SetupWizard />
{:else}
	<div class="app">
		<!-- First in the tab order, ahead of the sidebar, because every route
		     otherwise opens with the same four nav links in front of you. The
		     target takes tabindex="-1" so focus actually lands on <main>: a
		     fragment jump scrolls, and on some browsers that is all it does. -->
		<a class="skip-link" href="#main">Skip to main content</a>
		<Sidebar bind:collapsed={sidebarCollapsed} />

		<main id="main" tabindex="-1" class:collapsed={sidebarCollapsed}>
			<div class="route-content">
				{@render children()}
			</div>
		</main>

		<Toasts />
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

	/* The skip target is focused, so it would otherwise arrive wearing the global
	   focus ring around an entire page. Landing there is the point; drawing a
	   2px frame around the viewport is not the signal anyone asked for. */
	main:focus,
	main:focus-visible {
		outline: none;
	}

	main.collapsed {
		margin-left: 64px;
	}

	.route-content {
		flex: 1;
	}

	@media (max-width: 768px) {
		main {
			margin-left: 64px;
		}
	}
</style>
