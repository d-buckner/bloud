<script lang="ts">
	interface Props {
		name: string;
		size?: number;
	}

	let { name, size = 20 }: Props = $props();
</script>

<span
	class="icon"
	style="
		width: {size}px;
		height: {size}px;
		-webkit-mask-image: url('/icons/{name}.svg');
		mask-image: url('/icons/{name}.svg');
	"
></span>

<style>
	.icon {
		display: inline-block;
		background-color: currentColor;
		-webkit-mask-size: contain;
		mask-size: contain;
		-webkit-mask-repeat: no-repeat;
		mask-repeat: no-repeat;
		-webkit-mask-position: center;
		mask-position: center;
	}
</style>
