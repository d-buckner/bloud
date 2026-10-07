<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	// ClientAccessPanel renders the credentials an installed app publishes for a
	// client a human holds, entirely from the descriptor the backend returns.
	//
	// There is no per-app code here by design. The panel asks the generic
	// client-credentials endpoint what the app declared and renders that, so a
	// second app adopting the pattern needs no frontend change. If the app
	// declares nothing, the panel renders nothing.
	import Button from './Button.svelte';
	import { isAdmin } from '$lib/stores/user';

	interface Descriptor {
		secret: string;
		label: string;
		reveal: string; // never | once | always
		rotate: string; // bloud | provider | none
		reaches: string;
		snippet: string;
		revealed: boolean;
		published: boolean;
	}

	// The backend sends the policy strings and the published/revealed facts,
	// not the derived button visibility. Deriving here keeps the policy in one
	// place (the loader) and the panel a dumb renderer of it.
	function revealable(cred: Descriptor): boolean {
		return cred.published && cred.reveal !== 'never';
	}
	function rotateAllowed(cred: Descriptor): boolean {
		return cred.rotate === 'bloud' || cred.rotate === 'provider';
	}

	interface Props {
		appName: string;
	}

	let { appName }: Props = $props();

	let descriptors = $state<Descriptor[]>([]);
	let loading = $state(true);
	let busy = $state<string | null>(null);
	let error = $state<string | null>(null);
	let revealed = $state<Record<string, string>>({});
	let copied = $state<string | null>(null);

	// The panel only ever shows for an admin, and only after the app is
	// installed, so a non-admin never triggers a request that would 403.
	$effect(() => {
		if (!appName || !$isAdmin) return;
		load();
	});

	async function load() {
		loading = true;
		error = null;
		try {
			const res = await fetch(`/api/apps/${appName}/client-credentials`);
			if (!res.ok) {
				descriptors = [];
				return;
			}
			const data = await res.json();
			descriptors = data.credentials ?? [];
		} catch {
			descriptors = [];
		} finally {
			loading = false;
		}
	}

	async function reveal(name: string) {
		busy = name;
		error = null;
		try {
			const res = await fetch(`/api/apps/${appName}/client-credentials/${name}/reveal`, {
				method: 'POST'
			});
			if (!res.ok) {
				const body = await res.json().catch(() => ({}));
				error = body.error || `Could not reveal the credential (${res.status})`;
				return;
			}
			const data = await res.json();
			revealed[name] = data.value;
			await load();
		} catch {
			error = 'Could not reach the host agent.';
		} finally {
			busy = null;
		}
	}

	async function rotate(name: string) {
		busy = name;
		error = null;
		try {
			const res = await fetch(`/api/apps/${appName}/client-credentials/${name}/rotate`, {
				method: 'POST'
			});
			if (!res.ok) {
				const body = await res.json().catch(() => ({}));
				error = body.error || `Could not rotate the credential (${res.status})`;
				return;
			}
			// The old value is gone from the panel's view whether or not it was
			// showing, so a rotate cannot leave a stale secret on screen.
			delete revealed[name];
			await load();
		} catch {
			error = 'Could not reach the host agent.';
		} finally {
			busy = null;
		}
	}

	async function revoke(name: string) {
		busy = name;
		error = null;
		try {
			const res = await fetch(`/api/apps/${appName}/client-credentials/${name}/revoke`, {
				method: 'POST'
			});
			if (!res.ok) {
				const body = await res.json().catch(() => ({}));
				error = body.error || `Could not revoke sessions (${res.status})`;
				return;
			}
			await load();
		} catch {
			error = 'Could not reach the host agent.';
		} finally {
			busy = null;
		}
	}

	async function copy(name: string) {
		const value = revealed[name];
		if (!value) return;
		try {
			await navigator.clipboard.writeText(value);
			copied = name;
			setTimeout(() => (copied = null), 2000);
		} catch {
			error = 'Could not copy to the clipboard.';
		}
	}

	function confirmRevoke(name: string): boolean {
		const label = labelFor(name);
		return window.confirm(
			`End every active session for ${label}?\n\n` +
				'Every device signed in to this app will be logged out, including yours. ' +
				'The current password keeps working for new sign-ins: revoking sessions is ' +
				'not the same as changing it.'
		);
	}

	function labelFor(name: string): string {
		return descriptors.find((d) => d.secret === name)?.label || name;
	}
</script>

{#if $isAdmin && loading}
	<p class="loading-state">Loading client access...</p>
{:else if $isAdmin && descriptors.length > 0}
	{#if error}
		<div class="error-message">{error}</div>
	{/if}

	{#each descriptors as cred (cred.secret)}
		<div class="credential">
			<div class="credential-head">
				<span class="credential-label">{cred.label}</span>
				{#if cred.reveal === 'once' && cred.revealed}
					<span class="pill pill-muted">shown once</span>
				{:else if cred.reveal === 'never'}
					<span class="pill pill-muted">not shown</span>
				{/if}
			</div>

			{#if cred.reaches}
				<p class="credential-reaches">{cred.reaches}</p>
			{/if}

			{#if revealed[cred.secret]}
				<div class="revealed-value">
					<code>{revealed[cred.secret]}</code>
					<Button variant="secondary" size="sm" onclick={() => copy(cred.secret)}>
						{copied === cred.secret ? 'Copied' : 'Copy'}
					</Button>
				</div>
				{#if cred.reveal === 'once'}
					<p class="hint">
						This password will not be shown again. If you lose it, rotate to get one
						that can be shown. Devices already signed in keep working.
					</p>
				{/if}
			{:else if revealable(cred) && !cred.revealed}
				<Button variant="primary" size="sm" onclick={() => reveal(cred.secret)} disabled={busy !== null}>
					{busy === cred.secret ? 'Revealing…' : 'Reveal password'}
				</Button>
			{:else if cred.revealed && cred.reveal === 'once'}
				<p class="hint">
					This password was already revealed and cannot be shown again. Rotate to
					get a new one you can reveal.
				</p>
			{/if}

			<div class="credential-actions">
				{#if rotateAllowed(cred)}
					<Button variant="secondary" size="sm" onclick={() => rotate(cred.secret)} disabled={busy !== null}>
						{busy === cred.secret ? 'Rotating…' : 'Rotate password'}
					</Button>
				{/if}
				<Button
					variant="danger"
					size="sm"
					onclick={() => confirmRevoke(cred.secret) && revoke(cred.secret)}
					disabled={busy !== null}
				>
					Revoke sessions
				</Button>
			</div>

			<p class="hint">
				Rotating changes the password for new sign-ins. Sessions already running are
				not ended by it: use Revoke sessions for that.
			</p>
		</div>
	{/each}
{/if}

<style>
	.loading-state {
		padding: var(--space-xl);
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.9375rem;
	}

	.credential {
		padding: var(--space-lg);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		margin-bottom: var(--space-md);
	}

	.credential-head {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-bottom: var(--space-xs);
	}

	.credential-label {
		font-size: 0.9375rem;
		font-weight: 500;
	}

	.credential-reaches {
		margin: 0 0 var(--space-md) 0;
		color: var(--color-text-secondary);
		font-size: 0.875rem;
		line-height: 1.5;
	}

	.revealed-value {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-bottom: var(--space-sm);
	}

	.revealed-value code {
		flex: 1;
		padding: var(--space-sm) var(--space-md);
		background: var(--color-bg-subtle);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		word-break: break-all;
	}

	.hint {
		margin: var(--space-sm) 0 0 0;
		font-size: 0.8125rem;
		line-height: 1.4;
		color: var(--color-text-muted);
	}

	.credential-actions {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-top: var(--space-md);
	}

	.pill {
		display: inline-block;
		padding: 2px 8px;
		border-radius: 9999px;
		font-size: 0.6875rem;
		font-weight: 500;
		vertical-align: middle;
	}

	.pill-muted {
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
		border: 1px solid var(--color-border);
	}

	.error-message {
		margin-bottom: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		color: var(--color-error);
		background: var(--color-error-bg);
		border: 1px solid rgba(153, 27, 27, 0.15);
		border-radius: var(--radius-md);
	}
</style>
