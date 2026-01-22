<script lang="ts">
	import { browser } from '$app/environment';
	import type { WidgetProps } from './registry';

	let { config }: WidgetProps = $props();

	const STORAGE_KEY = 'bloud-quick-notes';

	let notes = $state('');
	let isSaving = $state(false);
	let lastSaved = $state<Date | null>(null);

	// Load notes from localStorage on mount
	$effect(() => {
		if (browser) {
			const saved = localStorage.getItem(STORAGE_KEY);
			if (saved) {
				notes = saved;
			}
		}
	});

	// Debounced save
	let saveTimeout: ReturnType<typeof setTimeout>;

	function handleInput(e: Event) {
		const target = e.target as HTMLTextAreaElement;
		notes = target.value;

		// Debounce save
		clearTimeout(saveTimeout);
		isSaving = true;
		saveTimeout = setTimeout(() => {
			if (browser) {
				localStorage.setItem(STORAGE_KEY, notes);
				lastSaved = new Date();
				isSaving = false;
			}
		}, 500);
	}
</script>

<div class="quick-notes">
	<textarea
		class="notes-input"
		placeholder="Jot down a quick note..."
		value={notes}
		oninput={handleInput}
	></textarea>
	<div class="notes-footer">
		{#if isSaving}
			<span class="save-status">Saving...</span>
		{:else if lastSaved}
			<span class="save-status">Saved</span>
		{/if}
	</div>
</div>

<style>
	.quick-notes {
		display: flex;
		flex-direction: column;
		height: 100%;
		gap: var(--space-xs);
	}

	.notes-input {
		flex: 1;
		width: 100%;
		padding: 0;
		border: none;
		background: transparent;
		resize: none;
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		line-height: 1.5;
		color: var(--color-text);
	}

	.notes-input::placeholder {
		color: var(--color-text-muted);
	}

	.notes-input:focus {
		outline: none;
	}

	.notes-footer {
		display: flex;
		justify-content: flex-end;
		min-height: 1rem;
	}

	.save-status {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
</style>
