<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import { createInvite, fetchGuests, createGuest } from '$lib/clients/sharingClient';
	import type { App, Guest } from '$lib/types';

	interface Props {
		app: App | null;
		onclose: () => void;
	}

	let { app, onclose }: Props = $props();

	let open = $derived(app !== null);

	// Guest state
	let guests = $state<Guest[]>([]);
	let selectedGuestId = $state('');
	let addingNewGuest = $state(false);
	let newGuestName = $state('');
	let loadingGuests = $state(false);

	// Form state
	let nodeShareLink = $state('');
	let submitting = $state(false);
	let errorMsg = $state('');

	// Token result state
	let token = $state('');
	let copied = $state(false);

	let phase = $derived(token ? 'token' : 'form');

	let selectedGuestName = $derived(
		guests.find((g) => g.id === selectedGuestId)?.name ?? ''
	);

	$effect(() => {
		if (open) {
			selectedGuestId = '';
			addingNewGuest = false;
			newGuestName = '';
			nodeShareLink = '';
			token = '';
			errorMsg = '';
			submitting = false;
			copied = false;
			loadGuests();
		}
	});

	async function loadGuests() {
		loadingGuests = true;
		try {
			guests = await fetchGuests();
		} catch {
			guests = [];
		} finally {
			loadingGuests = false;
		}
	}

	function handleGuestSelect(e: Event) {
		const value = (e.target as HTMLSelectElement).value;
		if (value === '__new__') {
			addingNewGuest = true;
			selectedGuestId = '';
			newGuestName = '';
		} else {
			addingNewGuest = false;
			selectedGuestId = value;
		}
	}

	async function handleAddGuest() {
		const name = newGuestName.trim();
		if (!name) return;
		errorMsg = '';
		try {
			const guest = await createGuest(name);
			guests = [...guests, guest];
			selectedGuestId = guest.id;
			addingNewGuest = false;
			newGuestName = '';
		} catch (err) {
			if (typeof err === 'object' && err !== null && 'message' in err) {
				errorMsg = (err as { message: string }).message;
			} else {
				errorMsg = 'Failed to create guest';
			}
		}
	}

	let canSubmit = $derived(
		selectedGuestId !== '' && nodeShareLink.trim() !== '' && !submitting
	);

	async function handleCreate() {
		if (!canSubmit || !app) return;
		submitting = true;
		errorMsg = '';
		try {
			const result = await createInvite(app.catalog_id, selectedGuestId, nodeShareLink.trim());
			token = result.token;
		} catch (err) {
			errorMsg = err instanceof Error ? err.message : 'Failed to create invite';
			if (typeof err === 'object' && err !== null && 'message' in err) {
				errorMsg = (err as { message: string }).message;
			}
		} finally {
			submitting = false;
		}
	}

	async function handleCopy() {
		await navigator.clipboard.writeText(token);
		copied = true;
		setTimeout(() => (copied = false), 2000);
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && canSubmit) {
			handleCreate();
		}
	}
</script>

<Modal {open} onclose={onclose}>
	<header class="modal-header">
		<h2>Share {app?.display_name ?? ''}</h2>
		<CloseButton onclick={onclose} />
	</header>

	{#if phase === 'form'}
		<div class="modal-body">
			<p class="description">
				Create an invite token to share this app with someone. They'll paste it into their Bloud instance.
			</p>

			<div class="field">
				<label for="share-guest">Guest</label>
				{#if loadingGuests}
					<select id="share-guest" disabled>
						<option>Loading...</option>
					</select>
				{:else if addingNewGuest}
					<div class="inline-add">
						<input
							type="text"
							bind:value={newGuestName}
							placeholder="Guest name"
							onkeydown={(e) => e.key === 'Enter' && handleAddGuest()}
						/>
						<button class="btn btn-sm" onclick={handleAddGuest} disabled={!newGuestName.trim()}>
							Add
						</button>
						<button class="btn btn-sm btn-secondary" onclick={() => (addingNewGuest = false)}>
							Cancel
						</button>
					</div>
				{:else}
					<select id="share-guest" value={selectedGuestId} onchange={handleGuestSelect}>
						<option value="" disabled>Select a guest...</option>
						{#each guests as guest}
							<option value={guest.id}>{guest.name}</option>
						{/each}
						<option value="__new__">+ Add new guest...</option>
					</select>
				{/if}
				<span class="hint">Who you're sharing this app with</span>
			</div>

			<div class="field">
				<label for="share-node-link">Tailscale share link</label>
				<input
					id="share-node-link"
					type="text"
					bind:value={nodeShareLink}
					onkeydown={handleKeydown}
					placeholder="https://login.tailscale.com/admin/invite/..."
				/>
				<span class="hint">The node share URL from Tailscale admin</span>
			</div>

			{#if errorMsg}
				<p class="error">{errorMsg}</p>
			{/if}
		</div>

		<footer class="modal-footer">
			<button class="btn btn-secondary" onclick={onclose}>Cancel</button>
			<button class="btn btn-primary" onclick={handleCreate} disabled={!canSubmit}>
				{submitting ? 'Creating...' : 'Create'}
			</button>
		</footer>
	{:else}
		<div class="modal-body">
			<p class="description">
				Copy this token and send it to <strong>{selectedGuestName}</strong>. They'll paste it into "Add Shared App" on their Bloud instance.
			</p>

			<div class="field">
				<label for="share-token">Invite token</label>
				<textarea
					id="share-token"
					class="token-output"
					readonly
					value={token}
					onclick={(e) => (e.target as HTMLTextAreaElement).select()}
				></textarea>
			</div>

			<button class="btn btn-copy" onclick={handleCopy}>
				{copied ? 'Copied!' : 'Copy token'}
			</button>
		</div>

		<footer class="modal-footer">
			<button class="btn btn-primary" onclick={onclose}>Done</button>
		</footer>
	{/if}
</Modal>

<style>
	.modal-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}

	.modal-header h2 {
		margin: 0;
		font-size: 1.125rem;
	}

	.modal-body {
		padding: var(--space-lg);
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.description {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-text-muted);
		line-height: 1.5;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.field label {
		font-size: 0.875rem;
		color: var(--color-text-muted);
	}

	.field input,
	.field select {
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		font-size: 1rem;
		font-family: var(--font-serif);
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
	}

	.field input:focus,
	.field select:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.inline-add {
		display: flex;
		gap: var(--space-xs);
		align-items: center;
	}

	.inline-add input {
		flex: 1;
		padding: var(--space-sm) var(--space-md);
		font-size: 1rem;
		font-family: var(--font-serif);
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
	}

	.inline-add input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.btn-sm {
		padding: var(--space-xs) var(--space-sm);
		font-size: 0.8125rem;
		white-space: nowrap;
	}

	.hint {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.token-output {
		width: 100%;
		min-height: 80px;
		padding: var(--space-sm) var(--space-md);
		font-size: 0.8125rem;
		font-family: monospace;
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
		resize: vertical;
		word-break: break-all;
	}

	.btn-copy {
		align-self: flex-start;
		padding: var(--space-xs) var(--space-md);
		border-radius: var(--radius-md);
		font-size: 0.8125rem;
		font-family: var(--font-serif);
		cursor: pointer;
		border: 1px solid var(--color-border);
		background: var(--color-bg-subtle);
		color: var(--color-text);
		transition: all 0.15s ease;
	}

	.btn-copy:hover {
		background: var(--color-bg);
	}

	.error {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-error);
	}

	.modal-footer {
		display: flex;
		gap: var(--space-sm);
		justify-content: flex-end;
		padding: var(--space-lg);
		border-top: 1px solid var(--color-border);
	}

	.btn {
		padding: var(--space-sm) var(--space-lg);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
		font-family: var(--font-serif);
		cursor: pointer;
		border: 1px solid transparent;
		transition: all 0.15s ease;
	}

	.btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.btn-secondary {
		background: var(--color-bg-subtle);
		color: var(--color-text);
		border-color: var(--color-border);
	}

	.btn-secondary:hover {
		background: var(--color-bg);
	}

	.btn-primary {
		background: var(--color-accent);
		color: white;
	}

	.btn-primary:hover:not(:disabled) {
		background: var(--color-accent-hover);
	}
</style>
