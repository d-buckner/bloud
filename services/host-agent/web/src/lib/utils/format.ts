// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * Display formatting shared by the dashboard widgets.
 *
 * Kept free of DOM and framework concerns so the boundary cases (unit
 * rollover, out-of-range percentages) are unit-testable.
 */

/** Binary size units, in ascending order. */
const SIZE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'] as const;

/**
 * Format a byte count for display: whole bytes below 1 KiB, one decimal place
 * above it so sizes read consistently ("900 B", "1.0 KB", "13.5 GB").
 */
export function formatBytes(bytes: number): string {
	if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';

	const exponent = Math.min(
		Math.floor(Math.log(bytes) / Math.log(1024)),
		SIZE_UNITS.length - 1
	);
	const value = bytes / 1024 ** exponent;

	return `${value.toFixed(exponent === 0 ? 0 : 1)} ${SIZE_UNITS[exponent]}`;
}

/**
 * Clamp a percentage into 0-100. Values are written straight into CSS widths,
 * so an out-of-range or missing reading must not break the layout.
 */
export function clampPercent(value: number): number {
	if (!Number.isFinite(value)) return 0;
	return Math.min(100, Math.max(0, value));
}

/** Format a 0-100 reading as a whole-number percentage label. */
export function formatPercent(value: number): string {
	return `${Math.round(clampPercent(value))}%`;
}
