<script lang="ts">
	import type { WidgetProps } from './registry';
	import {
		type WeatherData,
		type WeatherConfig,
		fetchWeather,
		getCachedWeather,
		cacheWeather,
		getWeatherDescription,
		defaultWeatherConfig,
	} from './weather';

	let { config, onConfigure }: WidgetProps = $props();

	let weather = $state<WeatherData | null>(null);
	let error = $state<string | null>(null);
	let loading = $state(true);

	function getConfig(): WeatherConfig {
		if (
			config &&
			typeof config.latitude === 'number' &&
			typeof config.longitude === 'number' &&
			typeof config.locationName === 'string'
		) {
			return {
				latitude: config.latitude,
				longitude: config.longitude,
				locationName: config.locationName,
			};
		}
		return defaultWeatherConfig;
	}

	async function loadWeather() {
		const weatherConfig = getConfig();

		// Try cache first
		const cached = getCachedWeather(weatherConfig);
		if (cached) {
			weather = cached;
			loading = false;
			return;
		}

		// Fetch fresh data
		try {
			loading = true;
			const data = await fetchWeather(weatherConfig);
			weather = data;
			cacheWeather(data, weatherConfig);
			error = null;
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load weather';
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		loadWeather();
		const interval = setInterval(loadWeather, 30 * 60 * 1000); // Refresh every 30 minutes
		return () => clearInterval(interval);
	});

	function formatDay(dateStr: string): string {
		const date = new Date(dateStr);
		return date.toLocaleDateString('en-US', { weekday: 'short' });
	}
</script>

<div class="weather-widget">
	{#if loading && !weather}
		<div class="loading">Loading weather...</div>
	{:else if error && !weather}
		<div class="error">
			<span>{error}</span>
			{#if onConfigure}
				<button class="configure-btn" onclick={onConfigure}>Configure Location</button>
			{/if}
		</div>
	{:else if weather}
		<div class="weather-content">
			<div class="current">
				<div class="current-main">
					<span class="temperature">{weather.temperature}°</span>
					<span class="condition">{getWeatherDescription(weather.weatherCode)}</span>
				</div>
				<div class="current-details">
					<span class="detail">Feels like {weather.apparentTemperature}°</span>
					<span class="detail">Humidity {weather.humidity}%</span>
				</div>
			</div>

			{#if weather.forecast.length > 0}
				<div class="forecast">
					{#each weather.forecast.slice(0, 4) as day}
						<div class="forecast-day">
							<span class="day-name">{formatDay(day.date)}</span>
							<span class="day-temps">
								<span class="temp-high">{day.tempMax}°</span>
								<span class="temp-low">{day.tempMin}°</span>
							</span>
						</div>
					{/each}
				</div>
			{/if}

			<div class="location">
				<span class="location-name">{getConfig().locationName}</span>
				{#if onConfigure}
					<button class="edit-btn" onclick={onConfigure} aria-label="Change location">
						<svg
							width="12"
							height="12"
							viewBox="0 0 24 24"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
						>
							<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
							<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
						</svg>
					</button>
				{/if}
			</div>
		</div>
	{/if}
</div>

<style>
	.weather-widget {
		min-height: 100px;
	}

	.loading,
	.error {
		color: var(--color-text-muted);
		font-size: 0.875rem;
		text-align: center;
		padding: var(--space-md);
	}

	.error {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		align-items: center;
	}

	.configure-btn {
		padding: var(--space-xs) var(--space-sm);
		background: var(--color-bg-subtle);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		font-size: 0.75rem;
		cursor: pointer;
		color: var(--color-text-secondary);
	}

	.configure-btn:hover {
		background: var(--color-bg);
		border-color: var(--color-border);
	}

	.weather-content {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.current {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.current-main {
		display: flex;
		align-items: baseline;
		gap: var(--space-sm);
	}

	.temperature {
		font-size: 2rem;
		font-weight: 600;
		line-height: 1;
		color: var(--color-text);
	}

	.condition {
		font-size: 0.9375rem;
		color: var(--color-text-secondary);
	}

	.current-details {
		display: flex;
		gap: var(--space-md);
	}

	.detail {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.forecast {
		display: flex;
		gap: var(--space-sm);
		padding-top: var(--space-sm);
		border-top: 1px solid var(--color-border-subtle);
	}

	.forecast-day {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-xs);
	}

	.day-name {
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--color-text-secondary);
		text-transform: uppercase;
		letter-spacing: 0.02em;
	}

	.day-temps {
		display: flex;
		gap: var(--space-xs);
		font-size: 0.8125rem;
	}

	.temp-high {
		color: var(--color-text);
		font-weight: 500;
	}

	.temp-low {
		color: var(--color-text-muted);
	}

	.location {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		padding-top: var(--space-sm);
		border-top: 1px solid var(--color-border-subtle);
	}

	.location-name {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.edit-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 20px;
		height: 20px;
		padding: 0;
		background: transparent;
		border: none;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
		opacity: 0.6;
		transition: opacity 0.15s ease;
	}

	.edit-btn:hover {
		opacity: 1;
	}
</style>
