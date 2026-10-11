// SPDX-License-Identifier: AGPL-3.0-only
import { get } from './httpClient';

export interface GraphNode {
	id: string;
	displayName: string;
	status: string;
	isSystem: boolean;
	nodeType: string; // "app" | "container" | "connection" | "service"
	/** Owning app's node ID; set on container nodes, which render inside the app's box. */
	parentId?: string;
	/**
	 * The lifecycle phase the engine reports for this node, empty when the engine
	 * tracks no node for it (an external provider, the ingress). This is the live
	 * answer; `status` is what the store recorded, and the two disagree while a
	 * pass is in flight.
	 */
	phase?: string;
	/** The failure text the engine recorded, when the node stopped on one. */
	reason?: string;
	/** A goroutine is working on this node right now. */
	inFlight?: boolean;
}

export interface GraphEdge {
	source: string;
	target: string;
	label: string;
}

export interface OrchestratorActivity {
	time: string;
	event: string;
	detail: string;
}

/** A node whose consecutive resync restarts crossed the warning threshold. */
export interface ResyncRestartSignal {
	node: string;
	reason: string;
	restarts: number;
	warnedAt: string;
}

export interface OrchestratorStatus {
	queueDepth: number;
	isConverging: boolean;
	recentActivity: OrchestratorActivity[];
	/** The intent loop has exited: every submit is accepted and nothing runs. */
	loopStopped?: boolean;
	/** When the most recent convergence pass completed; empty until the first. */
	lastConverged?: string;
	resyncRestartSignals?: ResyncRestartSignal[];
}

export interface DeveloperGraph {
	nodes: GraphNode[];
	edges: GraphEdge[];
	orchestrator?: OrchestratorStatus;
}

export function fetchDeveloperGraph(): Promise<DeveloperGraph> {
	return get<DeveloperGraph>('/api/system/developer');
}
