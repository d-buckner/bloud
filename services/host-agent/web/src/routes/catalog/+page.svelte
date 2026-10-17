<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	// The catalog is a modal over the dashboard, and this route is what gives it
	// a URL. The dashboard renders behind it so the scrim covers a real page
	// instead of an empty one, the URL is shareable, and the browser Back button
	// closes the modal. Closing navigates back to the dashboard.
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import HomeView from '$lib/components/HomeView.svelte';
	import CatalogModal from '$lib/components/CatalogModal.svelte';
</script>

<svelte:head>
	<title>Catalog · Bloud</title>
</svelte:head>

<HomeView />

<CatalogModal onclose={() => goto(resolve('/'))} />
