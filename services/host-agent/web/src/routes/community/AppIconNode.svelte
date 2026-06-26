<script lang="ts">
	import { Handle, Position } from '@xyflow/svelte';
	import AppIcon from '$lib/components/AppIcon.svelte';

	interface Props {
		data: {
			label: string;
			appId: string;
		};
	}

	let { data }: Props = $props();
</script>

<div class="app-icon-node">
	<Handle type="target" position={Position.Left} />
	<div class="icon-wrapper">
		<AppIcon appName={data.appId} displayName={data.label} size="md" />
	</div>
	<span class="label">{data.label}</span>
	<Handle type="source" position={Position.Right} />
</div>

<style>
	.app-icon-node {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 6px;
	}

	.icon-wrapper {
		width: 60px;
		height: 60px;
		border-radius: 12px;
		background: #fafaf9;
		border: 1px solid #e7e5e4;
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.label {
		font-size: 0.75rem;
		font-weight: 500;
		color: #44403c;
		max-width: 100px;
		text-align: center;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
</style>
