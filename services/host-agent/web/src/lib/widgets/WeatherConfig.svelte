<script lang="ts">
	import Modal from '$lib/components/Modal.svelte';
	import Button from '$lib/components/Button.svelte';
	import { geocodeCity, type WeatherConfig, defaultWeatherConfig } from './weather';

	interface Props {
		open: boolean;
		onclose: () => void;
		currentConfig: WeatherConfig;
		onSave: (config: WeatherConfig) => void;
	}

	let { open, onclose, currentConfig, onSave }: Props = $props();

	let cityInput = $state('');
	let searching = $state(false);
	let searchError = $state<string | null>(null);
	let selectedLocation = $state<WeatherConfig | null>(null);

	$effect(() => {
		if (open) {
			// Reset state when opening
			cityInput = '';
			searching = false;
			searchError = null;
			selectedLocation = null;
		}
	});

	async function handleSearch() {
		if (!cityInput.trim()) return;

		searching = true;
		searchError = null;

		try {
			const result = await geocodeCity(cityInput.trim());
			if (result) {
				selectedLocation = {
					latitude: result.latitude,
					longitude: result.longitude,
					locationName: result.name,
				};
			} else {
				searchError = 'City not found. Try a different search.';
			}
		} catch {
			searchError = 'Failed to search. Please try again.';
		} finally {
			searching = false;
		}
	}

	function handleSave() {
		if (selectedLocation) {
			onSave(selectedLocation);
			onclose();
		}
	}

	function handleUseDefault() {
		onSave(defaultWeatherConfig);
		onclose();
	}
</script>

<Modal {open} {onclose}>
	<div class="config">
		<header class="config-header">
			<h2 class="config-title">Weather Location</h2>
			<button class="close-btn" onclick={onclose} aria-label="Close">
				<svg
					width="20"
					height="20"
					viewBox="0 0 24 24"
					fill="none"
					stroke="currentColor"
					stroke-width="2"
				>
					<path d="M18 6L6 18M6 6l12 12" />
				</svg>
			</button>
		</header>

		<div class="config-body">
			<p class="config-description">Enter a city name to set your weather location.</p>

			<div class="search-form">
				<input
					type="text"
					class="search-input"
					placeholder="Enter city name..."
					bind:value={cityInput}
					onkeydown={(e) => e.key === 'Enter' && handleSearch()}
					disabled={searching}
				/>
				<Button variant="secondary" onclick={handleSearch} disabled={searching || !cityInput.trim()}>
					{searching ? 'Searching...' : 'Search'}
				</Button>
			</div>

			{#if searchError}
				<p class="error-message">{searchError}</p>
			{/if}

			{#if selectedLocation}
				<div class="selected-location">
					<div class="location-info">
						<span class="location-label">Selected:</span>
						<span class="location-name">{selectedLocation.locationName}</span>
					</div>
					<span class="location-coords">
						{selectedLocation.latitude.toFixed(2)}, {selectedLocation.longitude.toFixed(2)}
					</span>
				</div>
			{/if}

			<div class="current-location">
				<span class="current-label">Current location:</span>
				<span class="current-name">{currentConfig.locationName}</span>
			</div>
		</div>

		<footer class="config-footer">
			<button class="text-btn" onclick={handleUseDefault}>Use Default (NYC)</button>
			<div class="footer-actions">
				<Button variant="ghost" onclick={onclose}>Cancel</Button>
				<Button variant="primary" onclick={handleSave} disabled={!selectedLocation}>Save</Button>
			</div>
		</footer>
	</div>
</Modal>

<style>
	.config {
		display: flex;
		flex-direction: column;
	}

	.config-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: var(--space-lg);
		border-bottom: 1px solid var(--color-border-subtle);
	}

	.config-title {
		margin: 0;
		font-size: 1.125rem;
		font-weight: 500;
	}

	.close-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 32px;
		height: 32px;
		padding: 0;
		background: transparent;
		border: none;
		border-radius: var(--radius-md);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: background 0.15s ease, color 0.15s ease;
	}

	.close-btn:hover {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.config-body {
		padding: var(--space-lg);
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.config-description {
		margin: 0;
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
	}

	.search-form {
		display: flex;
		gap: var(--space-sm);
	}

	.search-input {
		flex: 1;
		padding: var(--space-sm) var(--space-md);
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
		color: var(--color-text);
	}

	.search-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.search-input:disabled {
		opacity: 0.6;
	}

	.error-message {
		margin: 0;
		color: var(--color-error, #dc2626);
		font-size: 0.875rem;
	}

	.selected-location {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		padding: var(--space-md);
		background: var(--color-bg-subtle);
		border-radius: var(--radius-md);
		border: 1px solid var(--color-accent);
	}

	.location-info {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.location-label {
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.02em;
	}

	.location-name {
		font-weight: 500;
		color: var(--color-text);
	}

	.location-coords {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		font-family: monospace;
	}

	.current-location {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding-top: var(--space-md);
		border-top: 1px solid var(--color-border-subtle);
	}

	.current-label {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.current-name {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.config-footer {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: var(--space-lg);
		border-top: 1px solid var(--color-border-subtle);
	}

	.text-btn {
		padding: 0;
		background: transparent;
		border: none;
		font-size: 0.875rem;
		color: var(--color-text-muted);
		cursor: pointer;
	}

	.text-btn:hover {
		color: var(--color-text-secondary);
		text-decoration: underline;
	}

	.footer-actions {
		display: flex;
		gap: var(--space-sm);
	}
</style>
