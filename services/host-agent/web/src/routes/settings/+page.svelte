<script lang="ts">
	import { onMount } from 'svelte';
	import {
		fetchTailnet,
		setTailnet,
		deleteTailnet,
		type TailnetConnection
	} from '$lib/clients/settingsClient';

	let connection = $state<TailnetConnection | null>(null);
	let loading = $state(true);
	let error = $state('');
	let saving = $state(false);

	// Form state
	let formName = $state('');
	let formType = $state<'tailscale' | 'headscale'>('tailscale');
	let formAuthKey = $state('');
	let formControlUrl = $state('');

	onMount(async () => {
		try {
			connection = await fetchTailnet();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load settings';
		} finally {
			loading = false;
		}
	});

	async function handleSave() {
		error = '';
		saving = true;
		try {
			connection = await setTailnet({
				name: formName,
				type: formType,
				authKey: formAuthKey,
				controlUrl: formType === 'headscale' ? formControlUrl : undefined
			});
			// Clear form on success
			formName = '';
			formAuthKey = '';
			formControlUrl = '';
		} catch (err: unknown) {
			const msg = err && typeof err === 'object' && 'message' in err
				? (err as { message: string }).message
				: 'Failed to save';
			error = msg;
		} finally {
			saving = false;
		}
	}

	async function handleRemove() {
		error = '';
		saving = true;
		try {
			await deleteTailnet();
			connection = null;
		} catch (err: unknown) {
			const msg = err && typeof err === 'object' && 'message' in err
				? (err as { message: string }).message
				: 'Failed to remove';
			error = msg;
		} finally {
			saving = false;
		}
	}
</script>

<svelte:head>
	<title>Settings · Bloud</title>
</svelte:head>

<div class="page">
	<header class="page-header">
		<div class="header-content">
			<h1>Settings</h1>
			<p class="subtitle">Configure your Bloud instance</p>
		</div>
	</header>

	<section class="section">
		<h2>Tailnet Connection</h2>
		<p class="section-description">
			Connect to a Tailscale or Headscale network to enable app sharing with other Bloud users.
		</p>

		{#if loading}
			<div class="loading-state">
				<p>Loading...</p>
			</div>
		{:else if connection}
			<div class="connection-card">
				<div class="connection-info">
					<div class="field">
						<span class="field-label">Name</span>
						<span class="field-value">{connection.name}</span>
					</div>
					<div class="field">
						<span class="field-label">Type</span>
						<span class="field-value type-badge">{connection.type}</span>
					</div>
					<div class="field">
						<span class="field-label">Auth Key</span>
						<span class="field-value mono">{connection.maskedAuthKey}</span>
					</div>
					{#if connection.controlUrl}
						<div class="field">
							<span class="field-label">Control URL</span>
							<span class="field-value mono">{connection.controlUrl}</span>
						</div>
					{/if}
				</div>
				<button class="btn btn-danger" onclick={handleRemove} disabled={saving}>
					{saving ? 'Removing...' : 'Remove'}
				</button>
			</div>
		{:else}
			<form class="tailnet-form" onsubmit={(e) => { e.preventDefault(); handleSave(); }}>
				<div class="form-field">
					<label for="tn-name">Connection Name</label>
					<input
						id="tn-name"
						type="text"
						placeholder="e.g. My Tailnet"
						bind:value={formName}
						required
					/>
				</div>

				<div class="form-field">
					<label for="tn-type">Type</label>
					<select id="tn-type" bind:value={formType}>
						<option value="tailscale">Tailscale</option>
						<option value="headscale">Headscale</option>
					</select>
				</div>

				<div class="form-field">
					<label for="tn-authkey">Auth Key</label>
					<input
						id="tn-authkey"
						type="password"
						placeholder="tskey-auth-..."
						bind:value={formAuthKey}
						required
					/>
				</div>

				{#if formType === 'headscale'}
					<div class="form-field">
						<label for="tn-controlurl">Control URL</label>
						<input
							id="tn-controlurl"
							type="url"
							placeholder="https://hs.example.com"
							bind:value={formControlUrl}
							required
						/>
					</div>
				{/if}

				<button class="btn btn-primary" type="submit" disabled={saving}>
					{saving ? 'Saving...' : 'Save'}
				</button>
			</form>
		{/if}

		{#if error}
			<div class="error-message">{error}</div>
		{/if}
	</section>
</div>

<style>
	.page {
		padding: var(--space-2xl) var(--space-xl);
	}

	.page-header {
		margin-bottom: var(--space-2xl);
		padding-bottom: var(--space-xl);
		border-bottom: 1px solid var(--color-border);
	}

	.header-content h1 {
		margin: 0;
		font-size: 1.75rem;
		font-weight: 500;
	}

	.subtitle {
		margin: var(--space-xs) 0 0 0;
		color: var(--color-text-muted);
		font-style: italic;
	}

	.section {
		max-width: 560px;
	}

	.section h2 {
		margin: 0 0 var(--space-xs) 0;
		font-size: 1.125rem;
		font-weight: 500;
	}

	.section-description {
		margin: 0 0 var(--space-xl) 0;
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
		line-height: 1.5;
	}

	.loading-state {
		padding: var(--space-xl);
		text-align: center;
		color: var(--color-text-muted);
	}

	/* Connection display */
	.connection-card {
		padding: var(--space-lg);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
	}

	.connection-info {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		margin-bottom: var(--space-lg);
	}

	.field {
		display: flex;
		align-items: baseline;
		gap: var(--space-md);
	}

	.field-label {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		min-width: 80px;
	}

	.field-value {
		font-size: 0.9375rem;
		color: var(--color-text);
	}

	.field-value.mono {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
	}

	.type-badge {
		text-transform: capitalize;
	}

	/* Form */
	.tailnet-form {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
	}

	.form-field {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.form-field label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.form-field input,
	.form-field select {
		padding: var(--space-sm) var(--space-md);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-bg-elevated);
		color: var(--color-text);
		transition: border-color 0.15s ease;
	}

	.form-field input:focus,
	.form-field select:focus {
		outline: none;
		border-color: var(--color-accent);
		box-shadow: 0 0 0 3px rgba(28, 25, 23, 0.08);
	}

	.form-field input::placeholder {
		color: var(--color-text-muted);
	}

	/* Buttons */
	.btn {
		padding: var(--space-sm) var(--space-lg);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		border: 1px solid transparent;
		border-radius: var(--radius-md);
		cursor: pointer;
		transition: all 0.15s ease;
		align-self: flex-start;
	}

	.btn:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.btn-primary {
		background: var(--color-accent);
		color: white;
	}

	.btn-primary:hover:not(:disabled) {
		opacity: 0.9;
	}

	.btn-danger {
		background: transparent;
		border-color: var(--color-error);
		color: var(--color-error);
	}

	.btn-danger:hover:not(:disabled) {
		background: var(--color-error);
		color: white;
	}

	/* Error */
	.error-message {
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		color: var(--color-error);
		background: rgba(220, 38, 38, 0.05);
		border: 1px solid rgba(220, 38, 38, 0.15);
		border-radius: var(--radius-md);
	}
</style>
