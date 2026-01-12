<script lang="ts">
	import Modal from './Modal.svelte';
	import AppIcon from './AppIcon.svelte';
	import CloseButton from './CloseButton.svelte';
	import Icon from './Icon.svelte';
	import type { CatalogApp, InstallPlan } from '$lib/types';

	interface Props {
		app: CatalogApp | null;
		status?: string | null;
		onclose: () => void;
		oninstall: (appName: string, choices: Record<string, string>) => void;
	}

	let { app, status = null, onclose, oninstall }: Props = $props();

	const installed = status === 'running' || status === 'error' || status === 'failed';
	const installing = status === 'installing' || status === 'starting';
	const isUninstalling = status === 'uninstalling';

	let installPlan = $state<InstallPlan | null>(null);
	let loadingPlan = $state(false);
	let planError = $state<string | null>(null);
	let choices = $state<Record<string, string>>({});
	let uninstallInProgress = $state(false);
	let uninstallError = $state<string | null>(null);

	$effect(() => {
		if (app && !installed) {
			loadInstallPlan(app.name);
		} else {
			resetState();
		}
	});

	function resetState() {
		installPlan = null;
		loadingPlan = false;
		planError = null;
		choices = {};
		uninstallInProgress = false;
		uninstallError = null;
	}

	async function doUninstall() {
		if (!app) return;

		uninstallInProgress = true;
		uninstallError = null;

		try {
			const res = await fetch(`/api/apps/${app.name}/uninstall`, { method: 'POST' });
			const result = await res.json();

			if (result.success) {
				handleClose();
			} else {
				uninstallError = result.error || 'Uninstall failed';
			}
		} catch (err) {
			uninstallError = err instanceof Error ? err.message : 'Uninstall failed';
		} finally {
			uninstallInProgress = false;
		}
	}

	async function loadInstallPlan(appName: string) {
		loadingPlan = true;
		planError = null;
		choices = {};

		try {
			const res = await fetch(`/api/apps/${appName}/plan-install`);
			const data = await res.json();

			if (!res.ok) {
				planError = data.error || `Failed to load install plan (${res.status})`;
				return;
			}

			installPlan = data;

			if (installPlan?.choices) {
				for (const choice of installPlan.choices) {
					if (choice.recommended) {
						choices[choice.integration] = choice.recommended;
					}
				}
			}
		} catch (err) {
			planError = err instanceof Error ? err.message : 'Failed to get install plan';
		} finally {
			loadingPlan = false;
		}
	}

	function doInstall() {
		if (!app) return;
		oninstall(app.name, choices);
		handleClose();
	}

	function handleClose() {
		resetState();
		onclose();
	}

	function formatAppName(name: string): string {
		return name.charAt(0).toUpperCase() + name.slice(1);
	}
</script>

<Modal open={app !== null} onclose={handleClose} size="lg">
	{#if app}
		<header class="modal-header">
			<div class="modal-app-header">
				<AppIcon appName={app.name} displayName={app.displayName} size="lg" />
				<div class="modal-app-info">
					<h2>{app.displayName || formatAppName(app.name)}</h2>
					{#if app.category}
						<span class="modal-app-category">{app.category}</span>
					{/if}
				</div>
			</div>
			<CloseButton onclick={handleClose} />
		</header>

		<div class="modal-body">
				{#if app.description}
					<p class="modal-description">{app.description}</p>
				{/if}

				{#if app.screenshots?.length}
					<div class="screenshots">
						{#each app.screenshots as screenshot}
							<img src={screenshot} alt="Screenshot" class="screenshot" />
						{/each}
					</div>
				{/if}

				{#if installed}
					{#if uninstallError}
						<div class="alert alert-error">
							<p>{uninstallError}</p>
						</div>
					{/if}

					<div class="installed-notice">
						<Icon name="check-circle" size={20} />
						<span>This app is installed</span>
					</div>
				{:else if loadingPlan}
					<div class="loading-plan">
						<p>Loading installation details...</p>
					</div>
				{:else if planError}
					<div class="alert alert-error">
						<p>{planError}</p>
					</div>
				{:else if installPlan}
					{#if !installPlan.canInstall}
						<div class="alert alert-error">
							<p><strong>Cannot install:</strong></p>
							<ul>
								{#each installPlan.blockers as blocker}
									<li>{blocker}</li>
								{/each}
							</ul>
						</div>
					{:else}
						{#if installPlan.choices.length > 0}
							<div class="choices-section">
								<h4>Configuration</h4>
								{#each installPlan.choices as choice}
									<div class="choice-field">
										<label for="choice-{choice.integration}">
											{choice.integration}
											{#if choice.required}<span class="required">*</span>{/if}
										</label>
										<select id="choice-{choice.integration}" bind:value={choices[choice.integration]}>
											<option value="">Select...</option>
											{#if choice.installed}
												<optgroup label="Installed">
													{#each choice.installed as opt}
														<option value={opt.app}>{formatAppName(opt.app)}</option>
													{/each}
												</optgroup>
											{/if}
											{#if choice.available}
												<optgroup label="Will be installed">
													{#each choice.available as opt}
														<option value={opt.app}>
															{formatAppName(opt.app)}
															{#if opt.default}(recommended){/if}
														</option>
													{/each}
												</optgroup>
											{/if}
										</select>
									</div>
								{/each}
							</div>
						{/if}
					{/if}
				{/if}
			</div>

			<footer class="modal-footer">
				{#if isUninstalling}
					<button class="btn btn-secondary" onclick={handleClose}>Close</button>
					<span class="status-text">Uninstalling...</span>
				{:else if installed}
					<button class="btn btn-secondary" onclick={handleClose} disabled={uninstallInProgress}>Close</button>
					<button class="btn btn-danger" onclick={doUninstall} disabled={uninstallInProgress}>
						{#if uninstallInProgress}Removing...{:else}Uninstall{/if}
					</button>
				{:else}
					<button class="btn btn-secondary" onclick={handleClose}>Cancel</button>
					{#if installPlan?.canInstall}
						<button class="btn btn-primary" onclick={doInstall} disabled={installing}>
							{#if installing}Getting...{:else}Get{/if}
						</button>
					{/if}
				{/if}
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

	.modal-app-header {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		flex: 1;
	}

	.modal-app-info h2 {
		margin: 0;
		font-size: 1.25rem;
		font-weight: 500;
	}

	.modal-app-category {
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		color: var(--color-text-muted);
	}

	.modal-body {
		padding: var(--space-lg);
	}

	.modal-footer {
		display: flex;
		gap: var(--space-sm);
		justify-content: flex-end;
		padding: var(--space-lg);
		border-top: 1px solid var(--color-border);
	}

	.modal-description {
		margin: 0 0 var(--space-lg) 0;
		font-size: 0.9375rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
	}

	.screenshots {
		display: flex;
		gap: var(--space-sm);
		overflow-x: auto;
		padding-bottom: var(--space-sm);
		margin-bottom: var(--space-lg);
	}

	.screenshot {
		flex-shrink: 0;
		width: 280px;
		height: auto;
		border-radius: var(--radius-md);
		border: 1px solid var(--color-border);
	}

	.installed-notice {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-md);
		background: var(--color-success-bg);
		color: var(--color-success);
		border-radius: var(--radius-md);
	}

	.loading-plan {
		padding: var(--space-lg);
		text-align: center;
		color: var(--color-text-muted);
		font-style: italic;
	}

	.choices-section {
		margin-bottom: var(--space-lg);
	}

	.choices-section h4 {
		margin: 0 0 var(--space-md) 0;
		font-size: 0.8125rem;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		color: var(--color-text-muted);
	}

	.choice-field {
		margin-bottom: var(--space-md);
	}

	.choice-field label {
		display: block;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		margin-bottom: var(--space-xs);
	}

	.required { color: var(--color-error); }

	.choice-field select {
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
		font-family: var(--font-serif);
		background: white;
	}

	.alert {
		padding: var(--space-md);
		border-radius: var(--radius-md);
		margin-bottom: var(--space-md);
		font-size: 0.9375rem;
	}

	.alert p { margin: var(--space-sm) 0; }
	.alert p:first-child { margin-top: 0; }
	.alert p:last-child { margin-bottom: 0; }
	.alert ul { margin: var(--space-sm) 0 0 0; padding-left: var(--space-lg); }

	.alert-error {
		background: var(--color-error-bg);
		color: var(--color-error);
	}

	.btn {
		display: inline-flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-lg);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
		font-family: var(--font-serif);
		cursor: pointer;
		border: 1px solid transparent;
		transition: all 0.15s ease;
	}

	.btn-primary {
		background: var(--color-accent);
		color: white;
	}

	.btn-primary:hover:not(:disabled) {
		background: var(--color-accent-hover);
	}

	.btn-primary:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.btn-secondary {
		background: var(--color-bg-elevated);
		color: var(--color-text);
		border-color: var(--color-border);
	}

	.btn-secondary:hover:not(:disabled) {
		background: var(--color-bg-subtle);
	}

	.btn-danger {
		background: var(--color-error);
		color: white;
	}

	.btn-danger:hover:not(:disabled) {
		background: #7f1d1d;
	}

	.btn-danger:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.status-text {
		font-size: 0.9375rem;
		color: var(--color-text-muted);
		font-style: italic;
	}
</style>
