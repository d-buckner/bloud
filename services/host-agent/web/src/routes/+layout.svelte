<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import '../app.css';
	import type { Snippet } from 'svelte';
	import { onMount } from 'svelte';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import SetupWizard from '$lib/components/SetupWizard.svelte';
	import Toasts from '$lib/components/Toasts.svelte';
	import { initApps, disconnectApps } from '$lib/services/appFacade';
	import { currentUser, type CurrentUser } from '$lib/stores/user';

	interface SetupStatus {
		setupRequired: boolean;
		authentikReady: boolean;
	}

	interface AuthMeResponse {
		id: number;
		username: string;
		role: 'admin' | 'member';
	}

	let { children }: { children: Snippet } = $props();

	let sidebarCollapsed = $state(false);
	let setupRequired = $state(false);
	let loading = $state(true);
	let user = $state<AuthMeResponse | null>(null);

	// Check setup status and auth, then initialize app if ready
	onMount(() => {
		checkStatusAndAuth();

		return () => {
			disconnectApps();
		};
	});

	async function checkStatusAndAuth() {
		try {
			// First check setup status
			const setupRes = await fetch('/api/setup/status');
			const setupData: SetupStatus = await setupRes.json();
			setupRequired = setupData.setupRequired;

			// If setup is required, don't check auth
			if (setupRequired) {
				loading = false;
				return;
			}

			// Check if user is authenticated
			const authRes = await fetch('/api/auth/me');
			if (authRes.ok) {
				user = await authRes.json();
				// Populate the global user store with role information
				if (user) {
					currentUser.set({
						username: user.username,
						role: user.role ?? 'admin'
					} as CurrentUser);
				}
			} else {
				// Not authenticated - redirect to login
				window.location.href = '/auth/login';
				return;
			}
		} catch {
			// If we can't reach the API, proceed with normal app (dev mode)
			setupRequired = false;
		}
		loading = false;

		// Only initialize app connections if setup is complete and user is authenticated
		if (!setupRequired && user) {
			initApps();
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
		<Sidebar bind:collapsed={sidebarCollapsed} {user} />

		<main class:collapsed={sidebarCollapsed}>
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
