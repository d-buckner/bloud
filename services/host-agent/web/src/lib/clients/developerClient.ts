import { get } from './httpClient';

export interface SidecarStatus {
	state: string;
	status: string;
}

export interface GraphNode {
	id: string;
	displayName: string;
	status: string;
	isSystem: boolean;
	sidecar: SidecarStatus | null;
}

export interface GraphEdge {
	source: string;
	target: string;
	label: string;
}

export interface DeveloperGraph {
	nodes: GraphNode[];
	edges: GraphEdge[];
}

export function fetchDeveloperGraph(): Promise<DeveloperGraph> {
	return get<DeveloperGraph>('/api/system/developer');
}
