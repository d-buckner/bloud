// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { EDGE_STYLE, edgeKind, showsLabel } from '../edgeKinds';
import { layoutGraph } from '../../timeline/graphLayout';
import type { DeveloperGraph } from '$lib/clients/developerClient';

/** #57534E on the canvas #FAF9F6: the one line the graph draws has to clear the
 * 3:1 a meaningful graphic needs (WCAG 1.4.11) without becoming the loudest
 * thing on the page. */
const contrastOnCanvas = (hex: string) => {
	const channel = (h: string, at: number) => {
		const c = parseInt(h.slice(at, at + 2), 16) / 255;
		return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
	};
	const lum = (h: string) =>
		0.2126 * channel(h, 1) + 0.7152 * channel(h, 3) + 0.0722 * channel(h, 5);
	const [hi, lo] = [lum(hex), lum('#FAF9F6')].sort((a, b) => b - a);
	return (hi + 0.05) / (lo + 0.05);
};

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
		expect(contrastOnCanvas(EDGE_STYLE.stroke)).toBeGreaterThanOrEqual(3);
	});

	it('drops the label a direction already carries', () => {
		expect(edges.find((e) => e.label === 'proxy')).toBeUndefined();
	});

	it('keeps the contract names readable off the line', () => {
		expect(edges.find((e) => e.source === 'hermes')?.label).toBe('agentApi');
	});
});
