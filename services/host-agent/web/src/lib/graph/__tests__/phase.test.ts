// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { isFailure, isUnprobed, isWorking, phaseWord, showsPhase } from '../phase';

// These five functions are the whole vocabulary of the page: what a card says,
// what a dot means, and when neither is a lie.

describe('phaseWord', () => {
	it('prefers what the engine is seeing over what the store recorded', () => {
		expect(phaseWord('starting', 'running')).toBe('starting');
	});

	it('falls back to the stored status when the engine tracks no node', () => {
		expect(phaseWord('', 'external')).toBe('external');
		expect(phaseWord(undefined, 'catalog')).toBe('catalog');
	});
});

describe('showsPhase', () => {
	it('keeps a settled state to the dot alone', () => {
		for (const phase of ['running', 'active', 'external', 'catalog']) {
			expect(showsPhase(phase)).toBe(false);
		}
	});

	it('words every state that is not done', () => {
		for (const phase of ['queued', 'configuring', 'starting', 'finalizing', 'failed']) {
			expect(showsPhase(phase)).toBe(true);
		}
	});
});

describe('isFailure', () => {
	it('names both spellings of stopped', () => {
		expect(isFailure('failed')).toBe(true);
		expect(isFailure('error')).toBe(true);
		expect(isFailure('running')).toBe(false);
	});
});

describe('isWorking', () => {
	it('pulses only what a goroutine is holding', () => {
		expect(isWorking(true, 'configuring')).toBe(true);
		expect(isWorking(false, 'configuring')).toBe(false);
	});

	it('never pulses a terminal node', () => {
		// ERROR is terminal until something resets it, so a pulse here would send
		// the reader to the one node nobody is touching.
		expect(isWorking(true, 'failed')).toBe(false);
	});
});

describe('isUnprobed', () => {
	it('is true only when the engine sent no phase', () => {
		expect(isUnprobed('')).toBe(true);
		expect(isUnprobed(undefined)).toBe(true);
		expect(isUnprobed('running')).toBe(false);
		// Queued is a reading, not the absence of one: the engine has the node.
		expect(isUnprobed('queued')).toBe(false);
	});
});
