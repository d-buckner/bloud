import { get } from './httpClient';

export interface GraphNode {
	id: string;
	displayName: string;
	status: string;
	isSystem: boolean;
	nodeType: string; // "app" | "connection"
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

export interface OrchestratorStatus {
	queueDepth: number;
	isConverging: boolean;
	recentActivity: OrchestratorActivity[];
}

// AppPhase is reserved for future use when per-node phase tracking is added.
export interface AppPhase {
	appName: string;
	phase: string;
	status: 'active' | 'done' | 'error' | 'warning';
}

export interface DeveloperGraph {
	nodes: GraphNode[];
	edges: GraphEdge[];
	tailnetDomain?: string;
	orchestrator?: OrchestratorStatus;
}

export function fetchDeveloperGraph(): Promise<DeveloperGraph> {
	return get<DeveloperGraph>('/api/system/developer');
}
