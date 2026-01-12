import { describe, it, expect, beforeEach, vi } from 'vitest';
import {
	getWeatherDescription,
	getWeatherIcon,
	getCachedWeather,
	cacheWeather,
	defaultWeatherConfig,
	type WeatherData,
	type WeatherConfig,
} from '../weather';

// Mock localStorage
const localStorageMock = (() => {
	let store: Record<string, string> = {};
	return {
		getItem: (key: string) => store[key] ?? null,
		setItem: (key: string, value: string) => {
			store[key] = value;
		},
		removeItem: (key: string) => {
			delete store[key];
		},
		clear: () => {
			store = {};
		},
	};
})();

Object.defineProperty(global, 'localStorage', {
	value: localStorageMock,
});

describe('weather logic', () => {
	beforeEach(() => {
		localStorageMock.clear();
		vi.restoreAllMocks();
	});

	describe('getWeatherDescription', () => {
		it('returns correct description for clear sky', () => {
			expect(getWeatherDescription(0)).toBe('Clear sky');
		});

		it('returns correct description for rain', () => {
			expect(getWeatherDescription(63)).toBe('Moderate rain');
		});

		it('returns correct description for thunderstorm', () => {
			expect(getWeatherDescription(95)).toBe('Thunderstorm');
		});

		it('returns Unknown for invalid code', () => {
			expect(getWeatherDescription(999)).toBe('Unknown');
		});
	});

	describe('getWeatherIcon', () => {
		it('returns sun for clear day', () => {
			expect(getWeatherIcon(0, true)).toBe('sun');
		});

		it('returns moon for clear night', () => {
			expect(getWeatherIcon(0, false)).toBe('moon');
		});

		it('returns cloud-sun for partly cloudy day', () => {
			expect(getWeatherIcon(2, true)).toBe('cloud-sun');
		});

		it('returns cloud-moon for partly cloudy night', () => {
			expect(getWeatherIcon(2, false)).toBe('cloud-moon');
		});

		it('returns cloud-rain for rain', () => {
			expect(getWeatherIcon(63, true)).toBe('cloud-rain');
		});

		it('returns cloud-snow for snow', () => {
			expect(getWeatherIcon(73, true)).toBe('cloud-snow');
		});

		it('returns cloud-lightning for thunderstorm', () => {
			expect(getWeatherIcon(95, true)).toBe('cloud-lightning');
		});
	});

	describe('caching', () => {
		const mockWeatherData: WeatherData = {
			temperature: 72,
			apparentTemperature: 75,
			weatherCode: 0,
			isDay: true,
			humidity: 45,
			windSpeed: 10,
			forecast: [
				{ date: '2024-01-02', tempMax: 75, tempMin: 55, weatherCode: 0 },
			],
		};

		const mockConfig: WeatherConfig = {
			latitude: 40.7128,
			longitude: -74.006,
			locationName: 'New York, NY',
		};

		it('returns null when no cache exists', () => {
			expect(getCachedWeather(mockConfig)).toBeNull();
		});

		it('caches and retrieves weather data', () => {
			cacheWeather(mockWeatherData, mockConfig);
			const cached = getCachedWeather(mockConfig);
			expect(cached).toEqual(mockWeatherData);
		});

		it('returns null when cache is expired', () => {
			// Cache data with old timestamp
			const oldCache = {
				data: mockWeatherData,
				timestamp: Date.now() - 31 * 60 * 1000, // 31 minutes ago
				config: mockConfig,
			};
			localStorage.setItem('bloud-weather-cache', JSON.stringify(oldCache));

			expect(getCachedWeather(mockConfig)).toBeNull();
		});

		it('returns null when location changed', () => {
			cacheWeather(mockWeatherData, mockConfig);

			const differentConfig: WeatherConfig = {
				latitude: 34.0522,
				longitude: -118.2437,
				locationName: 'Los Angeles, CA',
			};

			expect(getCachedWeather(differentConfig)).toBeNull();
		});

		it('returns cached data when within expiry and same location', () => {
			cacheWeather(mockWeatherData, mockConfig);

			// Request with same location
			const cached = getCachedWeather(mockConfig);
			expect(cached).not.toBeNull();
			expect(cached?.temperature).toBe(72);
		});

		it('handles invalid JSON in localStorage', () => {
			localStorage.setItem('bloud-weather-cache', 'invalid json');
			expect(getCachedWeather(mockConfig)).toBeNull();
		});
	});

	describe('defaultWeatherConfig', () => {
		it('has NYC coordinates', () => {
			expect(defaultWeatherConfig.latitude).toBeCloseTo(40.7128, 2);
			expect(defaultWeatherConfig.longitude).toBeCloseTo(-74.006, 2);
			expect(defaultWeatherConfig.locationName).toBe('New York, NY');
		});
	});
});
