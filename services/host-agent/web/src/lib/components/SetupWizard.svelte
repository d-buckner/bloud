<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Button from './Button.svelte';

	interface SetupStatus {
		setupRequired: boolean;
		authentikReady: boolean;
	}

	interface CreateUserResponse {
		success: boolean;
		error?: string;
		adoptedUrl?: string;
	}

	let username = $state('');
	let password = $state('');
	let confirmPassword = $state('');
	let error = $state('');
	let submitting = $state(false);
	let authentikReady = $state(false);
	let checkingStatus = $state(true);
	// Set once first-run setup has moved the primary host to the address this
	// install was actually reached on. That move re-provisions the identity
	// provider's redirect URIs, so the wizard waits for the provider to come
	// back before handing off to a login that would otherwise not be
	// registered yet.
	let finishingSetup = $state(false);
	let adoptedUrl = $state('');

	// Check if Authentik is ready on mount
	$effect(() => {
		checkAuthentikStatus();
		const interval = setInterval(checkAuthentikStatus, 5000);
		return () => clearInterval(interval);
	});

	// The handoff. Reloading is what takes the browser out of the wizard, and
	// it is deliberately gated on authentikReady rather than done inline: the
	// poller above is already reading the one signal that says the redirect
	// URIs for the new host are live.
	$effect(() => {
		if (finishingSetup && authentikReady) {
			window.location.reload();
		}
	});

	async function checkAuthentikStatus() {
		try {
			const res = await fetch('/api/setup/status');
			const data: SetupStatus = await res.json();
			authentikReady = data.authentikReady;
			checkingStatus = false;
		} catch {
			authentikReady = false;
			checkingStatus = false;
		}
	}

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();

		if (password !== confirmPassword) {
			error = 'Passwords do not match';
			return;
		}

		if (password.length < 8) {
			error = 'Password must be at least 8 characters';
			return;
		}

		submitting = true;
		error = '';

		try {
			const res = await fetch('/api/setup/create-user', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ username, password })
			});

			const data: CreateUserResponse = await res.json();

			if (data.success) {
				if (data.adoptedUrl) {
					finishingSetup = true;
					adoptedUrl = data.adoptedUrl;
					// Force the wait: the provider is about to be re-provisioned
					// for the new host, and the last known state is stale.
					authentikReady = false;
					return;
				}
				// Reload the page to trigger normal app flow
				window.location.reload();
			} else {
				error = data.error || 'Failed to create account';
				submitting = false;
			}
		} catch {
			error = 'Failed to connect to server';
			submitting = false;
		}
	}
</script>

<div class="setup-container">
	<div class="setup-card">
		<div class="setup-header">
			<h1>Welcome to Bloud</h1>
			<p>Create your admin account to get started.</p>
		</div>

		{#if finishingSetup}
			<div class="status-message">
				<span class="spinner"></span>
				<p>Account created. Pointing this install at <strong>{adoptedUrl}</strong>.</p>
				<p class="hint">
					Re-provisioning sign-in for that address. This takes a minute or two;
					you will be taken to your dashboard automatically.
				</p>
			</div>
		{:else if checkingStatus}
			<div class="status-message">
				<span class="spinner"></span>
				Checking system status...
			</div>
		{:else if !authentikReady}
			<div class="status-message warning">
				<p>Waiting for authentication service to start...</p>
				<p class="hint">This usually takes a minute on first boot.</p>
			</div>
		{:else}
			<form onsubmit={handleSubmit}>
				<div class="form-group">
					<label for="username">Username</label>
					<input
						type="text"
						id="username"
						bind:value={username}
						placeholder="admin"
						required
						minlength="3"
						maxlength="30"
						pattern="[a-zA-Z0-9_]+"
						autocomplete="username"
						disabled={submitting}
					/>
				</div>

				<div class="form-group">
					<label for="password">Password</label>
					<input
						type="password"
						id="password"
						bind:value={password}
						placeholder="Enter password"
						required
						minlength="8"
						autocomplete="new-password"
						disabled={submitting}
					/>
				</div>

				<div class="form-group">
					<label for="confirmPassword">Confirm Password</label>
					<input
						type="password"
						id="confirmPassword"
						bind:value={confirmPassword}
						placeholder="Confirm password"
						required
						minlength="8"
						autocomplete="new-password"
						disabled={submitting}
					/>
				</div>

				{#if error}
					<div class="error-message">{error}</div>
				{/if}

				<Button type="submit" disabled={submitting}>
					{submitting ? 'Creating Account...' : 'Create Account'}
				</Button>
			</form>
		{/if}
	</div>
</div>

<style>
	.setup-container {
		min-height: 100vh;
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--color-bg);
		padding: var(--space-lg);
	}

	.setup-card {
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		padding: var(--space-2xl);
		width: 100%;
		max-width: 400px;
		box-shadow: var(--shadow-md);
	}

	.setup-header {
		text-align: center;
		margin-bottom: var(--space-xl);
	}

	.setup-header h1 {
		font-family: var(--font-serif);
		font-size: 1.75rem;
		font-weight: 400;
		color: var(--color-text);
		margin: 0 0 var(--space-sm) 0;
	}

	.setup-header p {
		color: var(--color-text-secondary);
		margin: 0;
	}

	form {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	label {
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}

	input {
		padding: var(--space-sm) var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		font-size: 1rem;
		font-family: var(--font-sans);
		background: var(--color-bg);
		color: var(--color-text);
		transition: border-color 0.15s ease;
	}

	input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	input:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	input::placeholder {
		color: var(--color-text-muted);
	}

	.error-message {
		color: var(--color-error);
		font-size: 0.875rem;
		padding: var(--space-sm) var(--space-md);
		background: var(--color-error-bg);
		border-radius: var(--radius-md);
	}

	.status-message {
		text-align: center;
		color: var(--color-text-secondary);
		padding: var(--space-lg);
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-sm);
	}

	.status-message.warning {
		background: var(--color-warning-bg);
		border-radius: var(--radius-md);
		padding: var(--space-lg);
	}

	.status-message.warning p {
		margin: 0;
		color: var(--color-warning);
	}

	.status-message .hint {
		font-size: 0.875rem;
		opacity: 0.8;
	}

	.spinner {
		width: 20px;
		height: 20px;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
