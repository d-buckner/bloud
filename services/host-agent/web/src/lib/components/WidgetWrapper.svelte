<script lang="ts">
	import Widget from '$lib/widgets/Widget.svelte';
	import { type WidgetDefinition } from '$lib/widgets/registry';

	interface Props {
		widget: WidgetDefinition;
		onRemove: () => void;
	}

	let { widget, onRemove }: Props = $props();
</script>

<Widget title={widget.name} {onRemove}>
	<widget.component />
</Widget>
