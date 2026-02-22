<script lang="ts">
	import { Button, Input } from '@bloud/ui';

	interface InstallResponse {
		started: boolean;
		error?: string;
	}

	interface Props {
		selectedDisk: string;
		encryption: boolean;
		onInstallStarted: () => void;
	}

	let { selectedDisk, encryption, onInstallStarted }: Props = $props();

	const setupUser =
		typeof window !== 'undefined'
			? new URL(window.location.href).searchParams.get('setup_user') ?? ''
			: '';

	let username = $state(setupUser);
	let password = $state('');
	let confirmPassword = $state('');
	let validationError = $state('');
	let submitError = $state('');
	let submitting = $state(false);

	function validate(): string {
		const usernamePattern = /^[a-zA-Z0-9_]{3,30}$/;
		if (!usernamePattern.test(username)) {
			return 'Username must be 3–30 characters and contain only letters, numbers, or underscores.';
		}
		if (password.length < 8) {
			return 'Password must be at least 8 characters.';
		}
		if (password !== confirmPassword) {
			return 'Passwords do not match.';
		}
		return '';
	}

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();

		validationError = '';
		submitError = '';

		const err = validate();
		if (err) {
			validationError = err;
			return;
		}

		submitting = true;

		try {
			const res = await fetch('/api/install', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ disk: selectedDisk, username, password, encryption })
			});

			const data: InstallResponse = await res.json();

			if (!res.ok || !data.started) {
				submitError = data.error ?? 'Failed to start installation. Please try again.';
				submitting = false;
				return;
			}

			onInstallStarted();
		} catch {
			submitError = 'Could not connect to the installer service.';
			submitting = false;
		}
	}
</script>

<div class="card">
	<span class="wordmark">bloud</span>
	<h2 class="subtitle">Create your admin account.</h2>

	<form onsubmit={handleSubmit}>
		<div class="fields">
			<Input
				label="Username"
				type="text"
				bind:value={username}
				placeholder="admin"
				disabled={submitting}
			/>

			<Input
				label="Password"
				type="password"
				bind:value={password}
				placeholder="Enter password"
				disabled={submitting}
			/>

			<Input
				label="Confirm password"
				type="password"
				bind:value={confirmPassword}
				placeholder="Confirm password"
				disabled={submitting}
			/>
		</div>

		{#if validationError}
			<div class="error-box">{validationError}</div>
		{/if}

		{#if submitError}
			<div class="error-box">{submitError}</div>
		{/if}

		<Button type="submit" disabled={submitting}>
			{submitting ? 'Starting...' : 'Install Bloud'}
		</Button>
	</form>
</div>

<style>
	.card {
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		padding: var(--space-2xl);
		width: 100%;
		max-width: 420px;
		box-shadow: var(--shadow-md);
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
	}

	.wordmark {
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		font-weight: 400;
		letter-spacing: 0.08em;
		color: var(--color-text-secondary);
		text-transform: lowercase;
	}

	.subtitle {
		font-size: 1.375rem;
		font-weight: 400;
		color: var(--color-text);
		margin: 0;
		line-height: 1.3;
	}

	form {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.fields {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.error-box {
		color: var(--color-error);
		font-size: 0.875rem;
		padding: var(--space-sm) var(--space-md);
		background: var(--color-error-bg);
		border-radius: var(--radius-md);
	}
</style>
