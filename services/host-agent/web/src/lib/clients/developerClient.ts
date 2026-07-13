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

export interface DeveloperGraph {
	nodes: GraphNode[];
	edges: GraphEdge[];
	tailnetDomain?: string;
	orchestrator?: OrchestratorStatus;
}

export function fetchDeveloperGraph(): Promise<DeveloperGraph> {
	return get<DeveloperGraph>('/api/system/developer');
}
