// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The action registry, and the direction the guard cannot check for itself.
 *
 * The middleware refuses a type that is not on the list. What it cannot notice is
 * the opposite failure: a type that *is* on the list and that no reducer
 * implements. That action dispatches cleanly, changes nothing, and looks like a
 * feature that stopped working.
 *
 * So this test closes that end. It is a source check rather than a behavioural
 * one because dispatching every type from the initial state proves nothing: a
 * `COMMAND_SETTLED` with no record in flight is a legitimate no-op, and a test
 * that cannot tell those two no-ops apart is a test that has to be wrong about
 * one of them.
 */

import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, it, expect } from 'vitest';
import * as types from '../actionTypes';
import { KNOWN_ACTION_TYPES } from '../actions';

/**
 * Every file that can implement an action: the slices, the middleware, and the
 * kernel, which is where `dev/resync` is handled (it re-dispatches the last
 * snapshot rather than being a slice).
 */
function handlerSource(): string {
	const here = (rel: string) => fileURLToPath(new URL(rel, import.meta.url));
	const reducers = here('../reducers/');
	const files = [
		...readdirSync(reducers).map((f) => `${reducers}/${f}`),
		here('../middleware.ts'),
		here('../kernel.ts')
	];
	return files.map((f) => readFileSync(f, 'utf8')).join('\n');
}

describe('the action registry', () => {
	it('is exactly the set of declared types, with no duplicates', () => {
		expect(new Set(KNOWN_ACTION_TYPES).size).toBe(KNOWN_ACTION_TYPES.length);
		expect([...KNOWN_ACTION_TYPES].sort()).toEqual(Object.values(types).sort());
	});

	it('gives every declared type a reducer or a middleware that names it', () => {
		const source = handlerSource();
		const unhandled = Object.keys(types).filter((name) => !source.includes(name));
		// An action nothing implements is dead vocabulary: it dispatches, it
		// matches no case, and the dashboard silently stops responding to it.
		expect(unhandled).toEqual([]);
	});

	it('rejects a type nobody declared', () => {
		expect(KNOWN_ACTION_TYPES).not.toContain('apss/upserted');
	});
});
