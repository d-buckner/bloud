<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	// ClientAccessPanel renders the credentials an installed app publishes for a
	// client a human holds, entirely from the descriptor the backend returns.
	//
	// There is no per-app code here by design. The panel asks the generic
	// client-credentials endpoint what the app declared and renders that, so a
	// second app adopting the pattern needs no frontend change. If the app
	// declares nothing, the panel renders nothing.
	import Icon from './Icon.svelte';
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
	let loading = $state(false);
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

{#if $isAdmin && descriptors.length > 0}
	<section class="client-access">
		<h3 class="section-title">
			<Icon name="key" size={16} />
			<span>Client access</span>
		</h3>

		{#if error}
			<div class="alert alert-error" role="alert">
				<p>{error}</p>
			</div>
		{/if}

		{#each descriptors as cred (cred.secret)}
			<div class="credential">
				<div class="credential-head">
					<span class="credential-label">{cred.label}</span>
					{#if cred.reveal === 'once' && cred.revealed}
						<span class="badge badge-spent">shown</span>
					{:else if cred.reveal === 'never'}
						<span class="badge badge-hidden">not shown</span>
					{/if}
				</div>

				{#if cred.reaches}
					<p class="credential-reaches">{cred.reaches}</p>
				{/if}

				{#if revealed[cred.secret]}
					<div class="credential-value">
						<code>{revealed[cred.secret]}</code>
						<button
							class="btn btn-small"
							onclick={() => copy(cred.secret)}
							aria-label="Copy {cred.label}"
						>
							{copied === cred.secret ? 'Copied' : 'Copy'}
						</button>
					</div>
					{#if cred.reveal === 'once'}
						<p class="credential-note">
							This password will not be shown again. If it is lost, rotate to get a
							one that can be shown. Devices already signed in keep working.
						</p>
					{/if}
				{:else if revealable(cred)}
					<button
						class="btn btn-secondary btn-small"
						onclick={() => reveal(cred.secret)}
						disabled={busy !== null}
					>
						{busy === cred.secret ? 'Working...' : 'Reveal'}
					</button>
				{/if}

				<div class="credential-actions">
					{#if rotateAllowed(cred)}
						<button
							class="btn btn-secondary btn-small"
							onclick={() => rotate(cred.secret)}
							disabled={busy !== null}
						>
							{busy === cred.secret ? 'Working...' : 'Rotate password'}
						</button>
					{/if}
					<button
						class="btn btn-danger btn-small"
						onclick={() => confirmRevoke(cred.secret) && revoke(cred.secret)}
						disabled={busy !== null}
					>
						Revoke sessions
					</button>
				</div>

				<p class="credential-rotate-note">
					Rotating changes the password for new sign-ins. Sessions already running
					are not ended by it: use Revoke sessions for that.
				</p>
			</div>
		{/each}
	</section>
{/if}

<style>
	.client-access {
		margin-top: var(--space-lg);
		padding-top: var(--space-lg);
		border-top: 1px solid var(--color-border);
	}

	.section-title {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: var(--text-md);
		margin-bottom: var(--space-md);
	}

	.credential {
		padding: var(--space-md);
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
		font-weight: 600;
	}

	.credential-reaches {
		color: var(--color-text-muted);
		font-size: var(--text-sm);
		margin-bottom: var(--space-sm);
	}

	.credential-value {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-bottom: var(--space-sm);
	}

	.credential-value code {
		flex: 1;
		padding: var(--space-xs) var(--space-sm);
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm);
		word-break: break-all;
		font-family: var(--font-mono);
	}

	.credential-note,
	.credential-rotate-note {
		font-size: var(--text-xs);
		color: var(--color-text-muted);
		margin-top: var(--space-xs);
	}

	.credential-actions {
		display: flex;
		gap: var(--space-sm);
		margin-top: var(--space-sm);
	}

	.badge {
		font-size: var(--text-xs);
		padding: 2px 6px;
		border-radius: var(--radius-sm);
	}

	.badge-spent,
	.badge-hidden {
		background: var(--color-bg-secondary);
		color: var(--color-text-muted);
	}

	.btn-small {
		padding: var(--space-xs) var(--space-sm);
		font-size: var(--text-sm);
	}
</style>
