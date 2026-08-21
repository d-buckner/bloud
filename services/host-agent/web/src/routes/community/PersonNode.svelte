<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import { Handle, Position } from '@xyflow/svelte';

	interface Props {
		data: {
			label: string;
			isHost?: boolean;
		};
	}

	let { data }: Props = $props();

	let initial = $derived(data.label.charAt(0).toUpperCase());
</script>

<div class="person-node">
	<Handle type="target" position={Position.Left} />
	<div class="avatar" class:host={data.isHost}>
		<span class="initial">{initial}</span>
	</div>
	<span class="label">{data.label}</span>
	<Handle type="source" position={Position.Right} />
</div>

<style>
	.person-node {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 6px;
	}

	.avatar {
		width: 64px;
		height: 64px;
		border-radius: 50%;
		background: #e7e5e4;
		display: flex;
		align-items: center;
		justify-content: center;
		border: 2px solid #d6d3d1;
	}

	.avatar.host {
		background: #dbeafe;
		border-color: #93c5fd;
	}

	.initial {
		font-size: 1.5rem;
		font-weight: 600;
		color: #57534e;
	}

	.avatar.host .initial {
		color: #2563eb;
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
