<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import IconButton from './IconButton.svelte';

	interface Props {
		onclick: () => void;
	}

	let { onclick }: Props = $props();
</script>

<IconButton icon="close" size={20} {onclick} title="Close" variant="ghost" />
