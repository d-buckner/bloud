/**
 * Weather API logic using Open-Meteo (no API key required)
 * https://open-meteo.com/
 */

export interface WeatherData {
	temperature: number;
	apparentTemperature: number;
	weatherCode: number;
	isDay: boolean;
	humidity: number;
	windSpeed: number;
	forecast: ForecastDay[];
}

export interface ForecastDay {
	date: string;
	tempMax: number;
	tempMin: number;
	weatherCode: number;
}

export interface WeatherConfig {
	latitude: number;
	longitude: number;
	locationName: string;
}

interface CachedWeather {
	data: WeatherData;
	timestamp: number;
	config: WeatherConfig;
}

const CACHE_KEY = 'bloud-weather-cache';
const CACHE_DURATION_MS = 30 * 60 * 1000; // 30 minutes

/**
 * Get cached weather data if valid
 */
export function getCachedWeather(config: WeatherConfig): WeatherData | null {
	try {
		const cached = localStorage.getItem(CACHE_KEY);
		if (!cached) return null;

		const parsed: CachedWeather = JSON.parse(cached);

		// Check if cache is expired
		if (Date.now() - parsed.timestamp > CACHE_DURATION_MS) {
			return null;
		}

		// Check if config changed (different location)
		if (
			parsed.config.latitude !== config.latitude ||
			parsed.config.longitude !== config.longitude
		) {
			return null;
		}

		return parsed.data;
	} catch {
		return null;
	}
}

/**
 * Save weather data to cache
 */
export function cacheWeather(data: WeatherData, config: WeatherConfig): void {
	const cached: CachedWeather = {
		data,
		timestamp: Date.now(),
		config,
	};
	localStorage.setItem(CACHE_KEY, JSON.stringify(cached));
}

/**
 * Map Open-Meteo weather codes to descriptions
 * https://open-meteo.com/en/docs#weathervariables
 */
export function getWeatherDescription(code: number): string {
	const descriptions: Record<number, string> = {
		0: 'Clear sky',
		1: 'Mainly clear',
		2: 'Partly cloudy',
		3: 'Overcast',
		45: 'Fog',
		48: 'Depositing rime fog',
		51: 'Light drizzle',
		53: 'Moderate drizzle',
		55: 'Dense drizzle',
		56: 'Light freezing drizzle',
		57: 'Dense freezing drizzle',
		61: 'Slight rain',
		63: 'Moderate rain',
		65: 'Heavy rain',
		66: 'Light freezing rain',
		67: 'Heavy freezing rain',
		71: 'Slight snow',
		73: 'Moderate snow',
		75: 'Heavy snow',
		77: 'Snow grains',
		80: 'Slight rain showers',
		81: 'Moderate rain showers',
		82: 'Violent rain showers',
		85: 'Slight snow showers',
		86: 'Heavy snow showers',
		95: 'Thunderstorm',
		96: 'Thunderstorm with slight hail',
		99: 'Thunderstorm with heavy hail',
	};
	return descriptions[code] || 'Unknown';
}

/**
 * Get weather icon based on weather code and day/night
 */
export function getWeatherIcon(code: number, isDay: boolean): string {
	// Clear
	if (code === 0) return isDay ? 'sun' : 'moon';
	// Mainly clear / Partly cloudy
	if (code === 1 || code === 2) return isDay ? 'cloud-sun' : 'cloud-moon';
	// Overcast
	if (code === 3) return 'cloud';
	// Fog
	if (code === 45 || code === 48) return 'cloud-fog';
	// Drizzle
	if (code >= 51 && code <= 57) return 'cloud-drizzle';
	// Rain
	if (code >= 61 && code <= 67) return 'cloud-rain';
	// Snow
	if (code >= 71 && code <= 77) return 'cloud-snow';
	// Rain showers
	if (code >= 80 && code <= 82) return 'cloud-rain';
	// Snow showers
	if (code >= 85 && code <= 86) return 'cloud-snow';
	// Thunderstorm
	if (code >= 95) return 'cloud-lightning';

	return 'cloud';
}

interface OpenMeteoResponse {
	current: {
		temperature_2m: number;
		apparent_temperature: number;
		weather_code: number;
		is_day: number;
		relative_humidity_2m: number;
		wind_speed_10m: number;
	};
	daily: {
		time: string[];
		temperature_2m_max: number[];
		temperature_2m_min: number[];
		weather_code: number[];
	};
}

/**
 * Fetch weather data from Open-Meteo API
 */
export async function fetchWeather(config: WeatherConfig): Promise<WeatherData> {
	const url = new URL('https://api.open-meteo.com/v1/forecast');
	url.searchParams.set('latitude', config.latitude.toString());
	url.searchParams.set('longitude', config.longitude.toString());
	url.searchParams.set(
		'current',
		'temperature_2m,apparent_temperature,weather_code,is_day,relative_humidity_2m,wind_speed_10m'
	);
	url.searchParams.set('daily', 'temperature_2m_max,temperature_2m_min,weather_code');
	url.searchParams.set('timezone', 'auto');
	url.searchParams.set('forecast_days', '5');

	const response = await fetch(url.toString());
	if (!response.ok) {
		throw new Error(`Weather API error: ${response.status}`);
	}

	const data: OpenMeteoResponse = await response.json();

	const forecast: ForecastDay[] = data.daily.time.slice(1).map((date, i) => ({
		date,
		tempMax: Math.round(data.daily.temperature_2m_max[i + 1]),
		tempMin: Math.round(data.daily.temperature_2m_min[i + 1]),
		weatherCode: data.daily.weather_code[i + 1],
	}));

	return {
		temperature: Math.round(data.current.temperature_2m),
		apparentTemperature: Math.round(data.current.apparent_temperature),
		weatherCode: data.current.weather_code,
		isDay: data.current.is_day === 1,
		humidity: data.current.relative_humidity_2m,
		windSpeed: Math.round(data.current.wind_speed_10m),
		forecast,
	};
}

/**
 * Geocode a city name to coordinates using Open-Meteo Geocoding API
 */
export async function geocodeCity(
	city: string
): Promise<{ latitude: number; longitude: number; name: string } | null> {
	const url = new URL('https://geocoding-api.open-meteo.com/v1/search');
	url.searchParams.set('name', city);
	url.searchParams.set('count', '1');
	url.searchParams.set('language', 'en');
	url.searchParams.set('format', 'json');

	const response = await fetch(url.toString());
	if (!response.ok) {
		throw new Error(`Geocoding API error: ${response.status}`);
	}

	const data = await response.json();
	if (!data.results || data.results.length === 0) {
		return null;
	}

	const result = data.results[0];
	return {
		latitude: result.latitude,
		longitude: result.longitude,
		name: result.name + (result.admin1 ? `, ${result.admin1}` : ''),
	};
}

/**
 * Default config (New York City)
 */
export const defaultWeatherConfig: WeatherConfig = {
	latitude: 40.7128,
	longitude: -74.006,
	locationName: 'New York, NY',
};
