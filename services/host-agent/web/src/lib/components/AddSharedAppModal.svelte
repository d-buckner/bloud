<script lang="ts">
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import type { CatalogApp, InvitePayload } from '$lib/types';

	interface Props {
		open: boolean;
		catalogApps: CatalogApp[];
		onclose: () => void;
		onadd: (appId: string, tailnetAddr: string, hostLabel: string) => void;
	}

	let { open, catalogApps, onclose, onadd }: Props = $props();

	type Mode = 'token' | 'manual';
	let mode = $state<Mode>('token');

	// Manual mode state
	let appId = $state('');
	let tailnetAddr = $state('');
	let hostLabel = $state('');
	let submitting = $state(false);
	let errorMsg = $state('');

	// Token mode state
	let tokenInput = $state('');
	let decoded = $state<InvitePayload | null>(null);
	let decodeError = $state('');

	$effect(() => {
		if (open) {
			mode = 'token';
			appId = catalogApps.length > 0 ? catalogApps[0].catalogId : '';
			tailnetAddr = '';
			hostLabel = '';
			errorMsg = '';
			submitting = false;
			tokenInput = '';
			decoded = null;
			decodeError = '';
		}
	});

	$effect(() => {
		const raw = tokenInput.trim();
		if (!raw) {
			decoded = null;
			decodeError = '';
			return;
		}
		try {
			const json = atob(raw.replace(/-/g, '+').replace(/_/g, '/'));
			const payload = JSON.parse(json) as InvitePayload;
			if (!payload.appId || !payload.sidecarTailnetAddr || !payload.hostLabel) {
				decoded = null;
				decodeError = 'Invalid token: missing required fields';
				return;
			}
			decoded = payload;
			decodeError = '';
		} catch {
			decoded = null;
			decodeError = 'Invalid token';
		}
	});

	let canSubmitManual = $derived(
		appId !== '' && tailnetAddr.trim() !== '' && hostLabel.trim() !== '' && !submitting
	);

	let canSubmitToken = $derived(decoded !== null && !submitting);

	async function handleSubmitManual() {
		if (!canSubmitManual) return;
		submitting = true;
		errorMsg = '';
		try {
			onadd(appId, tailnetAddr.trim(), hostLabel.trim());
			onclose();
		} catch (err) {
			errorMsg = err instanceof Error ? err.message : 'Failed to add shared app';
		} finally {
			submitting = false;
		}
	}

	async function handleSubmitToken() {
		if (!canSubmitToken || !decoded) return;
		submitting = true;
		errorMsg = '';
		try {
			onadd(decoded.appId, decoded.sidecarTailnetAddr, decoded.hostLabel);
			onclose();
		} catch (err) {
			errorMsg = err instanceof Error ? err.message : 'Failed to add shared app';
		} finally {
			submitting = false;
		}
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && canSubmitManual) {
			handleSubmitManual();
		}
	}
</script>

<Modal {open} {onclose}>
	<header class="modal-header">
		<h2>Add Shared App</h2>
		<CloseButton onclick={onclose} />
	</header>

	<div class="modal-body">
		<div class="mode-toggle">
			<button
				class="mode-btn"
				class:active={mode === 'token'}
				onclick={() => (mode = 'token')}
			>
				Paste token
			</button>
			<button
				class="mode-btn"
				class:active={mode === 'manual'}
				onclick={() => (mode = 'manual')}
			>
				Manual entry
			</button>
		</div>

		{#if mode === 'token'}
			<div class="field">
				<label for="shared-token-input">Invite token</label>
				<textarea
					id="shared-token-input"
					class="token-input"
					bind:value={tokenInput}
					placeholder="Paste the invite token here..."
				></textarea>
			</div>

			{#if decoded}
				<div class="token-preview">
					<p class="preview-label">
						<strong>{decoded.hostLabel}</strong> wants to share <strong>{decoded.appName}</strong>
					</p>
					<dl class="preview-details">
						<dt>Tailnet address</dt>
						<dd>{decoded.sidecarTailnetAddr}</dd>
					</dl>
					{#if decoded.nodeShareLink}
						<a
							class="tailscale-link"
							href={decoded.nodeShareLink}
							target="_blank"
							rel="noopener noreferrer"
						>
							Accept Tailscale access
						</a>
					{/if}
				</div>
			{/if}

			{#if decodeError}
				<p class="error">{decodeError}</p>
			{/if}
		{:else}
			<p class="description">
				Add an app shared from another Bloud host. It will be proxied through your local Traefik.
			</p>

			<div class="field">
				<label for="shared-app-type">App type</label>
				<select id="shared-app-type" bind:value={appId}>
					{#each catalogApps as app}
						<option value={app.catalogId}>{app.displayName}</option>
					{/each}
				</select>
			</div>

			<div class="field">
				<label for="shared-tailnet-addr">Tailnet domain</label>
				<input
					id="shared-tailnet-addr"
					type="text"
					bind:value={tailnetAddr}
					onkeydown={handleKeydown}
					placeholder="ts-jellyfin.tail1275sa.ts.net"
				/>
			</div>

			<div class="field">
				<label for="shared-host-label">Label</label>
				<input
					id="shared-host-label"
					type="text"
					bind:value={hostLabel}
					onkeydown={handleKeydown}
					placeholder="Johan's server"
				/>
				<span class="hint">Used for display name and subdomain</span>
			</div>
		{/if}

		{#if errorMsg}
			<p class="error">{errorMsg}</p>
		{/if}
	</div>

	<footer class="modal-footer">
		<button class="btn btn-secondary" onclick={onclose}>Cancel</button>
		{#if mode === 'token'}
			<button class="btn btn-primary" onclick={handleSubmitToken} disabled={!canSubmitToken}>
				{submitting ? 'Adding...' : 'Add'}
			</button>
		{:else}
			<button class="btn btn-primary" onclick={handleSubmitManual} disabled={!canSubmitManual}>
				{submitting ? 'Adding...' : 'Add'}
			</button>
		{/if}
	</footer>
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

	.mode-toggle {
		display: flex;
		gap: 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		overflow: hidden;
	}

	.mode-btn {
		flex: 1;
		padding: var(--space-xs) var(--space-md);
		background: var(--color-bg);
		border: none;
		font-size: 0.8125rem;
		font-family: var(--font-serif);
		color: var(--color-text-muted);
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.mode-btn:not(:last-child) {
		border-right: 1px solid var(--color-border);
	}

	.mode-btn.active {
		background: var(--color-bg-subtle);
		color: var(--color-text);
		font-weight: 500;
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

	.token-input {
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

	.token-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.token-preview {
		padding: var(--space-md);
		background: var(--color-bg-subtle);
		border-radius: var(--radius-md);
		border: 1px solid var(--color-border);
	}

	.preview-label {
		margin: 0 0 var(--space-sm) 0;
		font-size: 0.875rem;
		line-height: 1.5;
	}

	.preview-details {
		margin: 0 0 var(--space-sm) 0;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.preview-details dt {
		font-weight: 500;
	}

	.preview-details dd {
		margin: 0;
		font-family: monospace;
	}

	.tailscale-link {
		display: inline-block;
		font-size: 0.8125rem;
		color: var(--color-accent);
		text-decoration: underline;
	}

	.hint {
		font-size: 0.75rem;
		color: var(--color-text-muted);
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
