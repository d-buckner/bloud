<script lang="ts">
	import IconButton from './IconButton.svelte';

	interface Props {
		onclick: () => void;
	}

	let { onclick }: Props = $props();
</script>

<IconButton icon="close" size={20} {onclick} title="Close" variant="ghost" />
