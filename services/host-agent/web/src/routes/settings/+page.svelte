<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { isAdmin } from '$lib/stores/user';
	import AddressSettingsSection from '$lib/components/AddressSettingsSection.svelte';
	import AISettingsSection from '$lib/components/AISettingsSection.svelte';
	import TailnetSettingsSection from '$lib/components/TailnetSettingsSection.svelte';
	import UsersSettingsSection from '$lib/components/UsersSettingsSection.svelte';

	// The page is a shell: the admin gate and the page header. Every settings
	// section is a component that owns its own state, its own load-on-mount, and
	// its own scoped styles. `ready` stays false for a non-admin, so no section
	// fires an admin-only request on a page that is already redirecting away.
	let ready = $state(false);

	onMount(async () => {
		// Redirect non-admins
		if (!$isAdmin) {
			goto(resolve('/'));
			return;
		}
		ready = true;
	});
</script>

<svelte:head>
	<title>Settings · Bloud</title>
</svelte:head>

<div class="page">
	<header class="page-header">
		<div class="header-content">
			<h1>Settings</h1>
			<p class="subtitle">Configure your Bloud instance</p>
		</div>
	</header>

	{#if ready}
		<AddressSettingsSection />
		<AISettingsSection />
		<TailnetSettingsSection />
		<UsersSettingsSection />
	{/if}
</div>

<style>
	.page {
		padding: var(--space-2xl) var(--space-xl);
	}

	.page-header {
		margin-bottom: var(--space-2xl);
		padding-bottom: var(--space-xl);
		border-bottom: 1px solid var(--color-border);
	}

	.header-content h1 {
		margin: 0;
		font-size: 1.75rem;
		font-weight: 500;
	}

	.subtitle {
		margin: var(--space-xs) 0 0 0;
		color: var(--color-text-muted);
		font-style: italic;
	}
</style>
