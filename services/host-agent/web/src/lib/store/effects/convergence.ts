// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Waiting for an intent to become a stored value.
 *
 * Every settings save here is an intent: the API answers 202 and the orchestrator
 * applies the change on its own queue. The 202 is a receipt, not a result, so a
 * form that reads once right after it reads the previous state and reports
 * success for a write that has not happened yet. The fix is a poll, and a poll is
 * a timer, and a component holding a timer is what
 * `scripts/no-component-timers.mjs` exists to stop.
 *
 * So the timer lives here and the meaning stays with the caller. `pollUntil`
 * decides when to read and for how long, then hands back the last value it saw
 * without judging it: only the caller knows what settled means for its own
 * setting, and only the caller can say what went wrong when the window closes.
 */

export interface PollWindow {
	/** Pause between reads. */
	intervalMs: number;
	/** How long to keep reading before giving up. */
	timeoutMs: number;
}

function delay(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

/**
 * Reads until `settled` accepts a value, or the window closes.
 *
 * The first read is immediate. Most saves have already landed by the time the
 * response is parsed, and a poll that always sleeps first makes a healthy save
 * wait for nothing.
 *
 * The exit is one condition rather than a flag: keep going while the value has
 * not settled and the window is open. Both endings then read off the returned
 * value, so "it converged" and "it ran out of time" are the same kind of fact and
 * cannot drift apart.
 */
export async function pollUntil<T>(
	read: () => Promise<T>,
	settled: (value: T) => boolean,
	window: PollWindow
): Promise<T> {
	let value = await read();
	const deadline = Date.now() + window.timeoutMs;
	while (!settled(value) && Date.now() < deadline) {
		await delay(window.intervalMs);
		value = await read();
	}
	return value;
}
