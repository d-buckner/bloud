<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import { onMount } from 'svelte';
	import { useSvelteFlow } from '@xyflow/svelte';

	let { key }: { key: string } = $props();

	const { fitView } = useSvelteFlow();

	// Re-fit when data changes
	$effect(() => {
		key;
		requestAnimationFrame(() => fitView({ padding: 0.15 }));
	});

	// Re-fit when container resizes
	onMount(() => {
		const el = document.querySelector('.svelte-flow') as HTMLElement | null;
		if (!el) return;

		const observer = new ResizeObserver(() => {
			fitView({ padding: 0.15 });
		});
		observer.observe(el);

		return () => observer.disconnect();
	});
</script>
