// SPDX-License-Identifier: AGPL-3.0-only
// See https://kit.svelte.dev/docs/types#app
// for information about these interfaces
import type { DeveloperGraph } from './lib/clients/developerClient';

declare global {
	namespace App {
		// interface Error {}
		// interface Locals {}
		// interface PageData {}
		// interface PageState {}
		// interface Platform {}
	}

	interface Window {
		/**
		 * Catalog graph injected by the README image renderer
		 * (`scripts/render-graph.mjs`), so the snapshot derived from the app
		 * metadata never has to be written into the served tree.
		 */
		__BLOUD_CATALOG_GRAPH__?: DeveloperGraph;
		/**
		 * Viewport handle the render harness uses to place the graph exactly.
		 * Published by `routes/graph/FlowBridge.svelte`.
		 */
		__BLOUD_GRAPH_VIEW__?: {
			setViewport: (viewport: { x: number; y: number; zoom: number }) => Promise<boolean>;
			getViewport: () => { x: number; y: number; zoom: number };
		};
	}
}

export {};
