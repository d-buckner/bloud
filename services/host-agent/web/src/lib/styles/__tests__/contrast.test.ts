// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

/**
 * The contrast contract, measured rather than asserted by eye.
 *
 * --color-text-muted was #A8A29E. On the cream canvas that measures 2.40:1 and
 * on a white card 2.52:1, which is not "low emphasis", it is unreadable, and it
 * was the single root cause of 52 failing text nodes across the four routes.
 * Nobody notices that while reading their own screen, because the person who
 * chose the colour was not the person who had to read it at arm's length.
 *
 * So the ratio is computed here from the same hex digits the browser gets. It
 * is a few lines of WCAG's own formula, and it turns a judgement call into a
 * number that fails the build.
 */

const SRC = new URL('../../..', import.meta.url).pathname;
const css = readFileSync(join(SRC, 'lib/styles/variables.css'), 'utf8');

function channel(c: number): number {
	const s = c / 255;
	return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
}

function luminance(hex: string): number {
	const h = hex.replace('#', '');
	const [r, g, b] = [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
	return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

function contrast(a: string, b: string): number {
	const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
	return (hi + 0.05) / (lo + 0.05);
}

/** Reads a `--token: #RRGGBB;` declaration out of the stylesheet. */
function token(name: string): string {
	const m = css.match(new RegExp(`--${name}:\\s*(#[0-9A-Fa-f]{6})\\s*;`));
	if (!m) throw new Error(`--${name} is not a 6-digit hex colour in variables.css`);
	return m[1];
}

const BG = token('color-bg');
const CARD = token('color-bg-elevated');
const SUBTLE = token('color-bg-subtle');
const ACCENT = token('color-accent');

describe('text tokens clear WCAG AA on every surface text sits on', () => {
	it.each(['color-text', 'color-text-secondary', 'color-text-muted'])('--%s', (name) => {
		const value = token(name);
		for (const [surface, hex] of [
			['canvas', BG],
			['card', CARD],
			['subtle', SUBTLE]
		] as const) {
			expect(contrast(value, hex), `--${name} ${value} on ${surface} ${hex}`).toBeGreaterThanOrEqual(
				4.5
			);
		}
	});

	it('keeps a visible step between secondary and muted', () => {
		// Three greys at the same weight is one grey. The hierarchy is the point
		// of having three tokens, so the step is pinned, not left to taste.
		const secondary = contrast(token('color-text-secondary'), BG);
		const muted = contrast(token('color-text-muted'), BG);
		expect(secondary - muted).toBeGreaterThanOrEqual(1.5);
	});
});

describe('the focus ring survives every surface it can land on', () => {
	// 3:1 is the WCAG 1.4.11 bar for a non-text indicator. The primary button is
	// in this list on purpose: --color-accent measures 1.00:1 against itself,
	// which is how an accent-coloured ring ends up invisible exactly where the
	// audit found it missing.
	it.each([
		['canvas', BG],
		['card', CARD],
		['subtle', SUBTLE],
		['primary button fill', ACCENT]
	])('is at least 3:1 on the %s', (_name, hex) => {
		expect(contrast(token('color-focus'), hex)).toBeGreaterThanOrEqual(3);
	});
});

describe('the tap target minimum is a token, not a per-component guess', () => {
	it('names 44px', () => {
		const m = css.match(/--tap-target-min:\s*(\d+)px/);
		expect(m, '--tap-target-min must be a px value').not.toBeNull();
		expect(Number(m![1])).toBeGreaterThanOrEqual(44);
	});
});
