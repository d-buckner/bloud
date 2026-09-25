<!--
	Hands the SvelteFlow instance to the render harness.

	`scripts/render-graph.mjs` needs to place the graph exactly: it sets the
	zoom and the pan so the picture is cropped to the graph itself instead of
	to whatever a viewport happens to hold. That is a viewport question, not a
	graph question, so it is answered from outside the component through this
	thin bridge rather than by teaching the page about rendering.
-->
<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import { useSvelteFlow } from '@xyflow/svelte';

	const flow = useSvelteFlow();

	onMount(() => {
		window.__BLOUD_GRAPH_VIEW__ = {
			setViewport: (viewport) => flow.setViewport(viewport, { duration: 0 }),
			getViewport: () => flow.getViewport()
		};
	});
</script>
