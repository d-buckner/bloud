// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { EDGE_STYLE, edgeKind, showsLabel } from '../edgeKinds';
import { layoutGraph } from '../../timeline/graphLayout';
import type { DeveloperGraph } from '$lib/clients/developerClient';

/**
 * The graph paints on the page canvas, so every color it draws names a palette
 * token. The graph has no palette of its own, and a literal is a color that
 * stops matching the page the moment the theme flips.
 *
 * WCAG 1.4.11 asks 3:1 of a meaningful graphic. The line is one pixel wide and
 * the label is ten pixels tall, so both qualify. Each is measured against the
 * canvas it actually sits on, in both palettes, because a dark theme checked
 * only in the light is the mistake that left the edge labels white.
 */
const SRC = new URL('../../..', import.meta.url).pathname;
const CSS = readFileSync(join(SRC, 'lib/styles/variables.css'), 'utf8').replace(
	/\/\*[\s\S]*?\*\//g,
	''
);
const DARK_SELECTOR = ":root[data-theme='dark']";
const PALETTES = ['light', 'dark'] as const;

type Palette = (typeof PALETTES)[number];

/** One palette's declarations, with the comments stripped out. */
function block(palette: Palette): string {
	const at = CSS.indexOf(DARK_SELECTOR);
	if (at < 0) throw new Error(`${DARK_SELECTOR} is missing from variables.css`);
	return palette === 'light' ? CSS.slice(0, at) : CSS.slice(at);
}

/** Reads a `--token: #RRGGBB;` declaration out of one palette. */
function token(name: string, palette: Palette): string {
	const m = block(palette).match(new RegExp(`${name}:\\s*(#[0-9A-Fa-f]{6})\\s*;`));
	if (!m) throw new Error(`${name} is not a 6-digit hex colour in the ${palette} palette`);
	return m[1];
}

/** The token a style string names: `var(--x)` reads back `--x`. */
function tokenIn(style: string): string {
	const m = style.match(/var\((--[\w-]+)\)/);
	if (!m) throw new Error(`that style names no token, so it cannot follow the theme: ${style}`);
	return m[1];
}

function contrast(fg: string, bg: string): number {
	const channel = (h: string, at: number) => {
		const c = parseInt(h.slice(at, at + 2), 16) / 255;
		return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
	};
	const lum = (h: string) =>
		0.2126 * channel(h, 1) + 0.7152 * channel(h, 3) + 0.0722 * channel(h, 5);
	const [hi, lo] = [lum(fg), lum(bg)].sort((a, b) => b - a);
	return (hi + 0.05) / (lo + 0.05);
}

describe('edgeKind', () => {
	it('reads the label the backend sends', () => {
		expect(edgeKind('route')).toBe('ingress');
		expect(edgeKind('proxy')).toBe('proxy');
		expect(edgeKind('native-oidc')).toBe('identity');
		expect(edgeKind('ldap')).toBe('identity');
		expect(edgeKind('forward-auth')).toBe('identity');
		expect(edgeKind('mcp')).toBe('integration');
		expect(edgeKind('pvr')).toBe('integration');
		expect(edgeKind('')).toBe('depends');
	});
});

describe('showsLabel', () => {
	it('keeps the names a reader has to read off the line', () => {
		expect(showsLabel('identity')).toBe(true);
		expect(showsLabel('integration')).toBe(true);
		expect(showsLabel('ingress')).toBe(true);
	});

	// Four boxes all labelled "proxy" is four words that identify nothing, and a
	// container-to-container arrow inside one box is scoped by that box.
	it('drops the words the arrow already says', () => {
		expect(showsLabel('proxy')).toBe(false);
		expect(showsLabel('depends')).toBe(false);
	});
});

describe('buildEdges through layoutGraph', () => {
	const graph: DeveloperGraph = {
		nodes: [
			{ id: 'traefik', displayName: 'Traefik', status: 'running', isSystem: true, nodeType: 'app' },
			{ id: 'hermes', displayName: 'Hermes', status: 'running', isSystem: false, nodeType: 'app' },
			{ id: 'hermes-webui', displayName: 'Hermes Web UI', status: 'running', isSystem: false, nodeType: 'app' }
		],
		edges: [
			{ source: 'traefik', target: 'hermes', label: 'proxy' },
			{ source: 'traefik', target: 'hermes-webui', label: 'proxy' },
			{ source: 'hermes', target: 'hermes-webui', label: 'agentApi' }
		]
	};
	const { edges } = layoutGraph(graph);

	it('draws every edge the same way', () => {
		const style = `stroke: ${EDGE_STYLE.stroke}; stroke-width: ${EDGE_STYLE.width}px;`;
		for (const edge of edges) {
			expect(edge.style).toBe(style);
			expect(edge.animated).toBeFalsy();
		}
	});

	// The stroke is the only line the graph draws. It has to clear 3:1 on the
	// canvas without becoming the loudest thing on the page, and it has to do
	// that once per palette.
	it('keeps the line visible on the canvas of each palette', () => {
		const stroke = tokenIn(EDGE_STYLE.stroke);
		for (const palette of PALETTES) {
			const ratio = contrast(token(stroke, palette), token('--color-bg', palette));
			expect(ratio, `${stroke} on --color-bg, ${palette}`).toBeGreaterThanOrEqual(3);
		}
	});

	// A label is a real box sitting on top of the lines, so it carries the canvas
	// color behind the words. A hex there is a white sticker on a dark diagram.
	it('keeps each label readable on the canvas of each palette', () => {
		const labelled = edges.find((e) => e.label === 'agentApi');
		if (!labelled?.labelStyle) throw new Error('the contract edge draws no label at all');
		expect(labelled.labelStyle).not.toMatch(/#[0-9A-Fa-f]{3,8}/);

		const words = tokenIn(labelled.labelStyle);
		const behind = tokenIn(labelled.labelStyle.slice(labelled.labelStyle.indexOf('background')));
		for (const palette of PALETTES) {
			const ratio = contrast(token(words, palette), token(behind, palette));
			expect(ratio, `${words} on ${behind}, ${palette}`).toBeGreaterThanOrEqual(3);
		}
	});

	it('drops the label a direction already carries', () => {
		expect(edges.find((e) => e.label === 'proxy')).toBeUndefined();
	});

	it('keeps the contract names readable off the line', () => {
		expect(edges.find((e) => e.source === 'hermes')?.label).toBe('agentApi');
	});
});
