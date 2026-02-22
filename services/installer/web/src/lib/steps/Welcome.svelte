<script lang="ts">
	import { Button } from '@bloud/ui';

	interface StatusResponse {
		phase: string;
		hostname: string;
		ipAddresses: string[];
		cpu: string;
		memoryGB: number;
	}

	interface Disk {
		device: string;
		sizeGB: number;
		model: string;
		hasExistingData: boolean;
	}

	interface DisksResponse {
		disks: Disk[];
		autoSelected: string;
		ambiguous: boolean;
	}

	interface Props {
		onInstallStarted: () => void;
	}

	let { onInstallStarted }: Props = $props();

	let status = $state<StatusResponse | null>(null);
	let disksData = $state<DisksResponse | null>(null);
	let loadError = $state('');
	let loading = $state(true);
	let installing = $state(false);
	let installError = $state('');
	let advancedOpen = $state(false);
	let selectedDisk = $state('');
	let encryption = $state(true);

	$effect(() => {
		loadData();
	});

	async function loadData() {
		loading = true;
		loadError = '';
		try {
			const [statusRes, disksRes] = await Promise.all([
				fetch('/api/status'),
				fetch('/api/disks')
			]);

			if (!statusRes.ok || !disksRes.ok) {
				loadError = 'Failed to load system information.';
				loading = false;
				return;
			}

			status = await statusRes.json();
			disksData = await disksRes.json();
			selectedDisk = disksData?.autoSelected ?? '';

			if (disksData?.ambiguous) {
				advancedOpen = true;
			}
		} catch {
			loadError = 'Could not connect to the installer service.';
		}
		loading = false;
	}

	function formatSize(gb: number): string {
		if (gb >= 1000) {
			return `${(gb / 1000).toFixed(1)} TB`;
		}
		return `${gb.toFixed(0)} GB`;
	}

	let selectedDiskInfo = $derived(
		disksData?.disks.find((d) => d.device === selectedDisk) ?? null
	);

	let showWarning = $derived(selectedDiskInfo?.hasExistingData ?? false);

	async function handleContinue() {
		installing = true;
		installError = '';
		try {
			const res = await fetch('/api/install', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ disk: selectedDisk, encryption, flakePath: '' })
			});
			if (!res.ok) {
				const err = await res.json().catch(() => ({ error: 'Unknown error' }));
				installError = err.error ?? 'Failed to start installation.';
				installing = false;
				return;
			}
			onInstallStarted();
		} catch {
			installError = 'Could not connect to the installer service.';
			installing = false;
		}
	}
</script>

<div class="card">
	<span class="wordmark">bloud</span>
	<h2 class="subtitle">Your server is ready to set up.</h2>

	{#if loading}
		<div class="loading-row">
			<span class="spinner" aria-label="Loading"></span>
			<span class="loading-text">Detecting hardware...</span>
		</div>
	{:else if loadError}
		<div class="error-box">
			<p>{loadError}</p>
			<Button variant="secondary" size="sm" onclick={loadData}>Retry</Button>
		</div>
	{:else if status && disksData}
		<div class="info-card">
			<div class="info-row">
				<span class="info-label">CPU</span>
				<span class="info-value">{status.cpu}</span>
			</div>
			{#if selectedDiskInfo}
				<div class="info-row">
					<span class="info-label">Disk</span>
					<span class="info-value">{selectedDiskInfo.model} · {formatSize(selectedDiskInfo.sizeGB)}</span>
				</div>
			{/if}
			{#if status.ipAddresses.length > 0}
				<div class="info-row">
					<span class="info-label">IP</span>
					<span class="info-value">{status.ipAddresses[0]}</span>
				</div>
			{/if}
		</div>

		{#if showWarning}
			<div class="warning-box">
				All existing data will be erased.
			</div>
		{/if}

		{#if disksData.ambiguous}
			<div class="disk-picker-section">
				<p class="disk-picker-label">Multiple drives detected — choose one:</p>
				<div class="disk-list">
					{#each disksData.disks as disk}
						<label class="disk-option" class:selected={selectedDisk === disk.device}>
							<input
								type="radio"
								name="disk"
								value={disk.device}
								bind:group={selectedDisk}
							/>
							<span class="disk-name">{disk.model}</span>
							<span class="disk-size">{formatSize(disk.sizeGB)}</span>
						</label>
					{/each}
				</div>
			</div>
		{/if}

		<div class="advanced-section">
			<button
				class="advanced-toggle"
				type="button"
				onclick={() => (advancedOpen = !advancedOpen)}
				aria-expanded={advancedOpen}
			>
				<span class="toggle-arrow" class:open={advancedOpen}>&#9656;</span>
				Advanced
			</button>

			{#if advancedOpen}
				<div class="advanced-content">
					{#if !disksData.ambiguous}
						<fieldset class="fieldset">
							<legend class="fieldset-legend">Drive</legend>
							<div class="disk-list">
								{#each disksData.disks as disk}
									<label class="disk-option" class:selected={selectedDisk === disk.device}>
										<input
											type="radio"
											name="disk"
											value={disk.device}
											bind:group={selectedDisk}
										/>
										<span class="disk-name">{disk.model}</span>
										<span class="disk-size">{formatSize(disk.sizeGB)}</span>
									</label>
								{/each}
							</div>
						</fieldset>
					{/if}

					<label class="encryption-toggle">
						<input type="checkbox" bind:checked={encryption} />
						<span class="toggle-label">
							<span class="toggle-title">Encrypt drive</span>
							<span class="toggle-desc">Recommended. Protects data if the machine is lost or stolen.</span>
						</span>
					</label>
				</div>
			{/if}
		</div>

		{#if installError}
			<div class="error-box">
				<p>{installError}</p>
			</div>
		{/if}

		<div class="footer">
			<Button onclick={handleContinue} disabled={!selectedDisk || installing}>
				{#if installing}
					Starting&hellip;
				{:else}
					Install Bloud
				{/if}
			</Button>
		</div>
	{/if}
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

	.loading-row {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
	}

	.spinner {
		display: inline-block;
		width: 1rem;
		height: 1rem;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 0.7s linear infinite;
		flex-shrink: 0;
	}

	.loading-text {
		color: var(--color-text-muted);
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	.error-box {
		background: var(--color-error-bg);
		border-radius: var(--radius-md);
		padding: var(--space-md);
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.error-box p {
		margin: 0;
		color: var(--color-error);
		font-size: 0.9375rem;
	}

	.info-card {
		background: var(--color-bg-subtle);
		border: 1px solid var(--color-border-subtle);
		border-radius: var(--radius-md);
		padding: var(--space-md) var(--space-lg);
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.info-row {
		display: flex;
		gap: var(--space-md);
		font-size: 0.9375rem;
		line-height: 1.5;
	}

	.info-label {
		color: var(--color-text-muted);
		min-width: 2.5rem;
		flex-shrink: 0;
	}

	.info-value {
		color: var(--color-text);
	}

	.warning-box {
		background: var(--color-warning-bg);
		color: var(--color-warning);
		border-radius: var(--radius-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
	}

	.disk-picker-section {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.disk-picker-label {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}

	.disk-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.disk-option {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		cursor: pointer;
		font-size: 0.9375rem;
		transition: background 0.1s ease, border-color 0.1s ease;
	}

	.disk-option:hover {
		background: var(--color-bg-subtle);
	}

	.disk-option.selected {
		border-color: var(--color-accent);
		background: var(--color-bg-subtle);
	}

	.disk-option input[type='radio'] {
		flex-shrink: 0;
	}

	.disk-name {
		flex: 1;
		color: var(--color-text);
	}

	.disk-size {
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.advanced-section {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.advanced-toggle {
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		font-family: var(--font-serif);
		transition: color 0.1s ease;
	}

	.advanced-toggle:hover {
		color: var(--color-text);
	}

	.toggle-arrow {
		display: inline-block;
		font-size: 0.7rem;
		transition: transform 0.15s ease;
		line-height: 1;
	}

	.toggle-arrow.open {
		transform: rotate(90deg);
	}

	.advanced-content {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
		padding-left: var(--space-md);
		border-left: 2px solid var(--color-border-subtle);
	}

	.fieldset {
		border: none;
		padding: 0;
		margin: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.fieldset-legend {
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		margin-bottom: var(--space-xs);
		float: left;
		width: 100%;
	}

	.encryption-toggle {
		display: flex;
		align-items: flex-start;
		gap: var(--space-sm);
		cursor: pointer;
	}

	.encryption-toggle input[type='checkbox'] {
		margin-top: 0.2rem;
		flex-shrink: 0;
	}

	.toggle-label {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.toggle-title {
		font-size: 0.9375rem;
		color: var(--color-text);
	}

	.toggle-desc {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.footer {
		display: flex;
		justify-content: flex-end;
	}
</style>
