<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { isAdmin, select } from '$lib/store';
	import AddressSettingsSection from '$lib/components/AddressSettingsSection.svelte';
	import AISettingsSection from '$lib/components/AISettingsSection.svelte';
	import TimezoneSettingsSection from '$lib/components/TimezoneSettingsSection.svelte';
	import UsersSettingsSection from '$lib/components/UsersSettingsSection.svelte';

	const admin = select(isAdmin);

	// The page is a shell: the admin gate and the page header. Every settings
	// section is a component that owns its own state, its own load-on-mount, and
	// its own scoped styles. `ready` stays false for a non-admin, so no section
	// fires an admin-only request on a page that is already redirecting away.
	// The gate reads the session slice rather than a mount-time snapshot, so a
	// role that arrives after this page mounted cannot leave a non-admin looking
	// at an admin form.
	const ready = $derived(admin.current);

	$effect(() => {
		if (!admin.current) void goto(resolve('/'));
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
		<div class="settings-grid">
			<AddressSettingsSection />
			<TimezoneSettingsSection />
			<AISettingsSection />
			<UsersSettingsSection />
		</div>
	{/if}
</div>

<style>
	/* The four sections flow into two columns once the window is wide enough for
	   both. A single 560px column left half the window empty on a large display,
	   which is the page with the most content using the least width. The two
	   short forms pair up; the AI and Users blocks span the full width because
	   each is a list, not a form. */
	.settings-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-2xl) var(--space-xl);
		align-items: start;
	}

	.settings-grid :global(.ai-section),
	.settings-grid :global(.users-section) {
		grid-column: 1 / -1;
	}

	@media (max-width: 900px) {
		.settings-grid {
			grid-template-columns: 1fr;
			gap: var(--space-xl);
		}
	}
</style>
