<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	// The dashboard is a component because two routes render it: the home route,
	// and `/catalog`, which shows the same dashboard with the catalog modal over
	// it. The view owns the grid and the tile menus; the route owns the title.
	import HomeView from '$lib/components/HomeView.svelte';
</script>

<svelte:head>
	<title>Home · Bloud</title>
</svelte:head>

<HomeView />
