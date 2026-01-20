<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		open: boolean;
		onclose: () => void;
		size?: 'default' | 'lg' | 'xl' | 'fullscreen';
		children: Snippet;
	}

	let { open, onclose, size = 'default', children }: Props = $props();
	let dialog: HTMLDialogElement;

	$effect(() => {
		if (!dialog) return;
		if (open) {
			dialog.showModal();
		} else {
			dialog.close();
		}
	});

	function handleClick(e: MouseEvent) {
		if (e.target === dialog) {
			onclose();
		}
	}

	function handleCancel(e: Event) {
		e.preventDefault();
		onclose();
	}
</script>

<dialog
	bind:this={dialog}
	class="modal"
	class:modal-lg={size === 'lg'}
	class:modal-xl={size === 'xl'}
	class:modal-fullscreen={size === 'fullscreen'}
	onclick={handleClick}
	oncancel={handleCancel}
>
	<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
	<div class="modal-content" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()} role="presentation">
		{#if open}
			{@render children()}
		{/if}
	</div>
</dialog>

<style>
	dialog.modal {
		padding: 0;
		border: none;
		border-radius: var(--radius-lg);
		width: 90%;
		max-width: 480px;
		max-height: 85vh;
		overflow: visible;
		background: transparent;
		box-shadow: var(--shadow-md), 0 20px 40px rgba(0, 0, 0, 0.15);
	}

	dialog.modal::backdrop {
		background: rgba(0, 0, 0, 0.4);
		backdrop-filter: blur(2px);
	}

	dialog.modal-lg {
		max-width: 540px;
	}

	dialog.modal-xl {
		max-width: 900px;
		max-height: 90vh;
	}

	dialog.modal-xl .modal-content {
		max-height: 90vh;
	}

	dialog.modal-fullscreen {
		width: 100%;
		max-width: 100%;
		height: 100%;
		max-height: 100%;
		border-radius: 0;
	}

	dialog.modal-fullscreen .modal-content {
		max-height: 100%;
		height: 100%;
		border-radius: 0;
	}

	.modal-content {
		background: var(--color-bg-elevated);
		border-radius: var(--radius-lg);
		max-height: 85vh;
		overflow-y: auto;
	}
</style>
