// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The dashboard's own lifecycle.
 *
 * Starting the session read and the stream is not a domain write, but it is a
 * write to the store, so it comes from the platform like every other one. The
 * page says when the dashboard lives, and the hook owns what that means.
 */

import { onMount } from 'svelte';
import { commands } from '../commands';
import { stopTransport } from '../effects/connection';

export function useDashboard(): void {
	onMount(() => {
		commands.startDashboard();
		return () => stopTransport();
	});
}
