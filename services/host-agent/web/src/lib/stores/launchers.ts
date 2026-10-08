// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Launchers Store - Operator-declared external app tiles that open a URL.
 *
 * Mirrors the backend's external_apps table (launcher kind only on the home
 * grid). Populated from the home snapshot, the same source that feeds the
 * `apps` and `grid` stores, so the three can never disagree about what is on
 * screen.
 */

import { writable } from 'svelte/store';
import type { Launcher } from '$lib/types';

export const launchers = writable<Launcher[]>([]);
