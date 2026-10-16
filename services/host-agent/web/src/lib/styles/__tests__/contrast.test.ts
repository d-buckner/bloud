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
 *
 * It runs over both palettes, because a second theme is a second set of every
 * number below. A dark theme that was picked by eye is a dark theme where
 * --color-text-muted is 2.40:1 again and nobody has read it at arm's length.
 */

const SRC = new URL('../../..', import.meta.url).pathname;
const rawCss = readFileSync(join(SRC, 'lib/styles/variables.css'), 'utf8');

/**
 * CSS comments out, because a comment names tokens the way a declaration does.
 *
 * `--color-success-bg: the card tint at #F0FDF4 sits 1.04:1 from white` reads as
 * a declaration to a regex, and the design notes are full of sentences shaped
 * like that one. The notes are the reason the values are what they are, so the
 * fix is to keep them and to read them as prose.
 */
const css = rawCss.replace(/\/\*[\s\S]*?\*\//g, '');

/** Where the second palette starts. Everything above it is the default theme. */
const DARK_SELECTOR = ":root[data-theme='dark']";

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

/** Reads a `--token: #RRGGBB;` declaration out of one theme's block. */
function token(block: string, name: string): string {
	const m = block.match(new RegExp(`--${name}:\\s*(#[0-9A-Fa-f]{6})\\s*;`));
	if (!m) throw new Error(`--${name} is not a 6-digit hex colour in that theme`);
	return m[1];
}

/** Every colour token a theme has to declare, by name. */
function tokenNames(block: string): string[] {
	return [...block.matchAll(/--(color-[a-z-]+):/g)].map((m) => m[1]).sort();
}

const lightCss = css.slice(0, css.indexOf(DARK_SELECTOR));
const darkCss = css.slice(css.indexOf(DARK_SELECTOR));

const THEMES = [
	{ name: 'light theme', block: lightCss },
	{ name: 'dark theme', block: darkCss }
] as const;

describe('both palettes declare the same tokens', () => {
	it('gives the dark theme an answer for every name the light theme uses', () => {
		// A token the dark block forgets is not a dark-mode bug a tester finds. It
		// is the light value showing through on a dark canvas, which is exactly
		// the near-black-on-near-black failure this file exists to catch.
		expect(tokenNames(darkCss)).toEqual(tokenNames(lightCss));
	});

	it('declares which palette each one is, so the browser paints the right chrome', () => {
		for (const { name, block } of THEMES) {
			expect(block, `${name} must set color-scheme`).toMatch(/color-scheme:\s*(light|dark);/);
		}
	});
});

for (const { name, block } of THEMES) {
	const BG = token(block, 'color-bg');
	const CARD = token(block, 'color-bg-elevated');
	const SUBTLE = token(block, 'color-bg-subtle');
	const ACCENT = token(block, 'color-accent');
	const surfaces = [
		['canvas', BG],
		['card', CARD],
		['subtle', SUBTLE]
	] as const;

	describe(`${name}: text tokens clear WCAG AA on every surface text sits on`, () => {
		it.each(['color-text', 'color-text-secondary', 'color-text-muted'])('--%s', (tokenName) => {
			const value = token(block, tokenName);
			for (const [surface, hex] of surfaces) {
				expect(contrast(value, hex), `--${tokenName} ${value} on ${surface} ${hex}`).toBeGreaterThanOrEqual(
					4.5
				);
			}
		});

		it('keeps a visible step between secondary and muted', () => {
			// Three greys at the same weight is one grey. The hierarchy is the point
			// of having three tokens, so the step is pinned, not left to taste.
			const secondary = contrast(token(block, 'color-text-secondary'), BG);
			const muted = contrast(token(block, 'color-text-muted'), BG);
			expect(secondary - muted).toBeGreaterThanOrEqual(1.5);
		});
	});

	describe(`${name}: the focus ring survives every surface it can land on`, () => {
		// 3:1 is the WCAG 1.4.11 bar for a non-text indicator. The primary button is
		// in this list on purpose: --color-accent measures 1.00:1 against itself,
		// which is how an accent-coloured ring ends up invisible exactly where the
		// audit found it missing. In the dark theme it is the other way round, and
		// the ring is a mid-tone in both because it has to clear a near-black canvas
		// and a near-white button at the same time.
		it.each([
			['canvas', BG],
			['card', CARD],
			['subtle', SUBTLE],
			['primary button fill', ACCENT]
		])('is at least 3:1 on the %s', (_surface, hex) => {
			expect(contrast(token(block, 'color-focus'), hex)).toBeGreaterThanOrEqual(3);
		});
	});

	describe(`${name}: a filled control carries its own label colour`, () => {
		// `color: white` used to be written into every filled button. A fill that
		// inverts between palettes cannot keep a hardcoded label, so the pair is a
		// token and this is where the pairing is pinned.
		it.each([
			['--color-on-accent', 'color-on-accent', 'color-accent'],
			['--color-on-accent on hover', 'color-on-accent', 'color-accent-hover'],
			['--color-on-danger', 'color-on-danger', 'color-danger'],
			['--color-on-danger on hover', 'color-on-danger', 'color-danger-hover'],
			['--color-on-success', 'color-on-success', 'color-success']
		])('%s is at least 4.5:1 on its fill', (label, fg, bg) => {
			expect(contrast(token(block, fg), token(block, bg)), label).toBeGreaterThanOrEqual(4.5);
		});
	});

	describe(`${name}: a status colour works as a word and as a badge`, () => {
		// A status is drawn twice: the word next to a dot, and the dot itself on a
		// tinted fill. Both have to clear the text bar, because the same token is
		// used for both and a status that only works in one of them is a status
		// that goes missing on the surfaces that use the fill.
		it.each(['color-success', 'color-error', 'color-warning', 'color-info'])('--%s', (status) => {
			const value = token(block, status);
			for (const [surface, hex] of [surfaces[0], surfaces[1]]) {
				expect(contrast(value, hex), `--${status} on ${surface}`).toBeGreaterThanOrEqual(4.5);
			}
			expect(contrast(value, token(block, `${status}-bg`)), `--${status} on its own fill`).toBeGreaterThanOrEqual(
				4.5
			);
		});

		it('keeps the installed badge readable on its tint', () => {
			expect(contrast(token(block, 'color-success'), token(block, 'color-success-tint'))).toBeGreaterThanOrEqual(
				4.5
			);
		});
	});

	describe(`${name}: a skeleton block is perceptible on every surface it can sit on`, () => {
		// Not a WCAG bar: a skeleton carries no meaning on its own. But a block
		// that is the same colour as the surface behind it is an empty page, and
		// the whole point of the skeleton is that it is not one.
		it.each(surfaces)('shows a step on the %s', (_surface, hex) => {
			expect(contrast(token(block, 'color-skeleton'), hex)).toBeGreaterThanOrEqual(1.15);
		});
	});
}

describe('the tap target minimum is a token, not a per-component guess', () => {
	it('names 44px', () => {
		const m = css.match(/--tap-target-min:\s*(\d+)px/);
		expect(m, '--tap-target-min must be a px value').not.toBeNull();
		expect(Number(m![1])).toBeGreaterThanOrEqual(44);
	});
});
