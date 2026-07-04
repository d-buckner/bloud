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

export interface ReconcilerActivity {
	time: string;
	event: string;
	detail: string;
}

export interface AppPhase {
	appName: string;
	phase: string;
	status: 'active' | 'done' | 'error' | 'warning';
}

export interface ReconcilerStatus {
	queueDepth: number;
	isConverging: boolean;
	recentActivity: ReconcilerActivity[];
	appPhases?: AppPhase[];
}

export interface DeveloperGraph {
	nodes: GraphNode[];
	edges: GraphEdge[];
	tailnetDomain?: string;
	reconciler?: ReconcilerStatus;
}

export function fetchDeveloperGraph(): Promise<DeveloperGraph> {
	return get<DeveloperGraph>('/api/system/developer');
}
