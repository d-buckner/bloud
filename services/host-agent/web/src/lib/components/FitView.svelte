<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { useNodesInitialized, useStore, useSvelteFlow } from '@xyflow/svelte';

	let {
		fitToken = 0,
		autoFit = true,
		padding = 0.15,
		duration = 250
	}: {
		/** Bump to ask for a fit. Only a change fits; the value itself is opaque. */
		fitToken?: number;
		/** Whether a container resize may move the viewport. */
		autoFit?: boolean;
		padding?: number;
		duration?: number;
	} = $props();

	const { fitView } = useSvelteFlow();
	const store = useStore();
	const nodesInitialized = useNodesInitialized();

	// Zero means "no fit seen yet", so a component that mounts with a token already
	// spent on the first load still frames the graph.
	let lastToken = 0;

	/**
	 * A fit asked for before the canvas can answer is not lost, just deferred:
	 * `fitView` has nothing to measure against until the new nodes have dimensions
	 * and the zoom behavior exists, so this effect depends on both and fires the
	 * fit as soon as they do.
	 */
	$effect(() => {
		const token = fitToken;
		if (token === lastToken) return;
		if (!nodesInitialized.current || !store.panZoom) return;
		lastToken = token;
		requestAnimationFrame(() => fitView({ padding, duration }));
	});

	// A resize re-frames only a view nobody has moved, and not the zero-size pass
	// the observer always reports first: that one is the mount, which `fitToken`
	// owns.
	$effect(() => {
		const el = store.domNode;
		if (!el) return;
		let measured = false;
		const observer = new ResizeObserver(() => {
			if (!measured) {
				measured = true;
				return;
			}
			if (autoFit && nodesInitialized.current) fitView({ padding, duration });
		});
		observer.observe(el);
		return () => observer.disconnect();
	});
</script>
