<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import '../app.css';
	import type { Snippet } from 'svelte';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import Icon from '$lib/components/Icon.svelte';
	import LoadingShell from '$lib/components/LoadingShell.svelte';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import SetupWizard from '$lib/components/SetupWizard.svelte';
	import Toasts from '$lib/components/Toasts.svelte';
	import { applyTheme, theme, watchSystemTheme } from '$lib/theme.svelte';
	import { select, useDashboard } from '$lib/store';

	let { children }: { children: Snippet } = $props();

	let sidebarCollapsed = $state(false);
	// The mobile drawer. It is a separate state from `sidebarCollapsed`: below the
	// breakpoint the rail is off-canvas and always labelled, so a persisted
	// collapsed desktop rail must not strip a phone's navigation names.
	let mobileNavOpen = $state(false);

	const currentPath = $derived(page.url.pathname);
	// Any navigation closes the drawer. Reading the path is the trigger.
	$effect(() => {
		currentPath;
		mobileNavOpen = false;
	});

	// The session read and the stream are the store's business now. This page only
	// says when the dashboard starts and stops, and renders the four states the
	// session slice can be in: not asked yet, anonymous, needs setup, ready.
	const session = select((s) => s.session);

	// The platform owns what starting the dashboard means. The page only says
	// when it lives.
	useDashboard();

	onMount(() => {
		// The inline script in app.html already put the palette on the document.
		// This says it again, so a page that got here without that script (a strict
		// content policy, a cached document) still lands on the right theme, and it
		// starts the listener that follows the operating system.
		applyTheme(theme.resolved);
		const stopWatchingSystem = watchSystemTheme();
		return stopWatchingSystem;
	});
</script>

{#if session.current.status === 'bootstrapping'}
	<!-- The shell, in blocks, rather than a spinner on an empty canvas.

	     The session read is what this state waits on, and on a cold start it is a
	     second or more of a page that shows nothing. Nothing in the shell depends
	     on the answer: the rail, the header, and the grid are the same shape for
	     every visitor. The route children stay out of this on purpose. A route
	     that renders before the session is known reads admin rights as "not an
	     admin" and redirects Settings to Home, which is a worse bug than the
	     spinner it replaces.

	     The setup wizard and the sign-in redirect still win when the answer says
	     so; they just arrive over a page instead of over a blank one. -->
	<LoadingShell />
{:else if session.current.setupRequired}
	<SetupWizard />
{:else}
	<div class="app">
		<!-- First in the tab order, ahead of the sidebar, because every route
		     otherwise opens with the same four nav links in front of you. The
		     target takes tabindex="-1" so focus actually lands on <main>: a
		     fragment jump scrolls, and on some browsers that is all it does. -->
		<a class="skip-link" href="#main">Skip to main content</a>

		<!-- Narrow screens get a bar with a real button instead of a permanent
		     64px icon rail with no way to read it. -->
		<header class="mobile-bar">
			<button
				class="mobile-menu"
				type="button"
				aria-label="Open navigation"
				aria-expanded={mobileNavOpen}
				onclick={() => (mobileNavOpen = true)}
			>
				<Icon name="menu" size={22} />
			</button>
			<span class="mobile-title">Bloud</span>
		</header>

		<Sidebar bind:collapsed={sidebarCollapsed} bind:open={mobileNavOpen} />

		{#if mobileNavOpen}
			<button
				class="scrim"
				type="button"
				aria-label="Close navigation"
				onclick={() => (mobileNavOpen = false)}
			></button>
		{/if}

		<main id="main" tabindex="-1" class:collapsed={sidebarCollapsed}>
			<div class="route-content">
				{@render children()}
			</div>
		</main>

		<Toasts />
	</div>
{/if}

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

	.mobile-bar,
	.scrim {
		display: none;
	}

	@media (max-width: 768px) {
		main,
		main.collapsed {
			margin-left: 0;
			/* Clear the fixed bar. */
			padding-top: 56px;
		}

		.mobile-bar {
			display: flex;
			align-items: center;
			gap: var(--space-xs);
			position: fixed;
			top: 0;
			left: 0;
			right: 0;
			height: 56px;
			padding: 0 var(--space-sm);
			background: var(--color-bg-subtle);
			border-bottom: 1px solid var(--color-border);
			z-index: 60;
		}

		.mobile-menu {
			display: inline-flex;
			align-items: center;
			justify-content: center;
			background: transparent;
			border: none;
			border-radius: var(--radius-sm);
			color: var(--color-text-secondary);
			cursor: pointer;
		}

		.mobile-title {
			font-size: 1.125rem;
			font-weight: 500;
		}

		.scrim {
			display: block;
			position: fixed;
			inset: 0;
			z-index: 70;
			background: var(--color-scrim);
			border: none;
			cursor: pointer;
		}
	}
</style>
