// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * Layout Client - HTTP transport layer for layout save operations.
 *
 * PUT /api/user/layout is called by GridStackGrid after every drag/resize
 * settle and after widget add/remove. The full settled layout is always sent.
 */

import type { GridElement } from '$lib/types';

/**
 * Save the full settled layout to the backend.
 * Errors are swallowed — layout saves are best-effort; the next user interaction
 * will retry. Unauthorized responses are silently ignored (login redirect
 * is handled at the app level).
 */
export async function saveLayout(elements: GridElement[]): Promise<void> {
	try {
		await fetch('/api/user/layout', {
			method: 'PUT',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(elements),
		});
	} catch {
		// Silently ignore — transient network error
	}
}
