// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * Sharing Client - HTTP transport for sharing and guest management
 */

import { get, post } from './httpClient';
import type { Guest } from '$lib/types';

interface CreateInviteRequest {
	appId: string;
	guestId: string;
	nodeShareLink: string;
}

interface CreateInviteResponse {
	shareId: string;
	token: string;
}

interface GuestsResponse {
	guests: Guest[];
}

export interface CommunityNode {
	id: string;
	label: string;
	nodeType: 'person' | 'app';
	appId?: string;
}

export interface CommunityEdge {
	source: string;
	target: string;
}

export interface CommunityGraph {
	nodes: CommunityNode[];
	edges: CommunityEdge[];
}

/**
 * Create an invite token for sharing an app
 */
export function createInvite(appId: string, guestId: string, nodeShareLink: string): Promise<CreateInviteResponse> {
	return post<CreateInviteResponse>('/api/sharing/invites', {
		appId,
		guestId,
		nodeShareLink
	} satisfies CreateInviteRequest);
}

/**
 * Fetch all guests
 */
export async function fetchGuests(): Promise<Guest[]> {
	const resp = await get<GuestsResponse>('/api/sharing/guests');
	return resp.guests;
}

/**
 * Create a new guest
 */
export async function createGuest(name: string): Promise<Guest> {
	return post<Guest>('/api/sharing/guests', { name });
}

/**
 * Fetch the community sharing graph
 */
export function fetchCommunityGraph(): Promise<CommunityGraph> {
	return get<CommunityGraph>('/api/sharing/community');
}
