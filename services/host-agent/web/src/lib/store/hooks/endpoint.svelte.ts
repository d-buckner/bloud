// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The single-endpoint capability hook: transcription or embeddings.
 *
 * One call gives the form its three fields, the write-only key, the stored
 * facts, the probe that lists models, the save, the remove, and the chat address
 * offered as a one-click fill. The fill is read here rather than passed down from
 * the AI section, because a copy passed down needs a callback and an effect to
 * stay current.
 */

import { onMount } from 'svelte';
import { discoverEndpointModels } from '$lib/clients/settingsClient';
import type { EndpointContract, EndpointSettings } from '$lib/clients/settingsClient';
import { buildSaveRequest } from '$lib/utils/endpointSave';
import { select } from '../bindings.svelte';
import type { Readable } from '../bindings.svelte';
import { commands } from '../commands';
import { sameText } from '../draft.svelte';
import { field, isLoading, readError } from '../field.svelte';
import type { Field } from '../field.svelte';
import { aiView, endpointView } from '../selectors';
import type { ResourceView } from '../selectors';
import type { AISettings } from '$lib/clients/settingsClient';

/** One endpoint contract: its fields, its probe, and the chat address as a fill. */
export interface EndpointForm {
	name: Field<string>;
	baseUrl: Field<string>;
	model: Field<string>;
	/** Write-only, as above. */
	apiKey: Field<string>;
	readonly confirmed: EndpointSettings | null;
	readonly configured: boolean;
	readonly hasApiKey: boolean;
	readonly servedTo: string[];
	readonly dirty: boolean;
	readonly loading: boolean;
	readonly error: string;
	readonly models: string[];
	readonly testing: boolean;
	readonly testResult: string;
	readonly testOk: boolean | null;
	/** The chat upstream address, offered as a one-click fill. Empty when unknown. */
	readonly aiBaseUrl: string;
	/** The fill would change the field. */
	readonly canUseAIUpstream: boolean;
	save(): void;
	remove(): void;
	test(): void;
	useAIUpstream(): void;
}

class EndpointState implements EndpointForm {
	readonly name: Field<string>;
	readonly baseUrl: Field<string>;
	readonly model: Field<string>;
	readonly apiKey: Field<string>;

	private probed = $state<string[]>([]);
	private listing = $state(false);
	private listResult = $state('');
	private listOk = $state<boolean | null>(null);

	constructor(
		private readonly view: Readable<ResourceView<EndpointSettings>>,
		private readonly ai: Readable<ResourceView<AISettings>>,
		private readonly contract: EndpointContract,
		private readonly defaultName: string
	) {
		this.name = field({ live: () => this.live()?.name || defaultName, same: sameText });
		this.baseUrl = field({ live: () => this.live()?.baseUrl ?? '', same: sameText });
		this.model = field({ live: () => this.live()?.model ?? '', same: sameText });
		this.apiKey = field({ live: () => '' });
	}

	private live(): EndpointSettings | null {
		return this.view.current.value;
	}

	get confirmed(): EndpointSettings | null {
		return this.view.current.confirmed;
	}
	get configured(): boolean {
		return this.live()?.configured ?? false;
	}
	get hasApiKey(): boolean {
		return this.live()?.hasApiKey ?? false;
	}
	get servedTo(): string[] {
		return this.view.current.confirmed?.servedTo ?? [];
	}
	get dirty(): boolean {
		return this.name.dirty || this.baseUrl.dirty || this.model.dirty || this.apiKey.dirty;
	}
	get loading(): boolean {
		return isLoading(this.view.current);
	}
	get error(): string {
		return readError(this.view.current);
	}
	get models(): string[] {
		return this.probed;
	}
	get testing(): boolean {
		return this.listing;
	}
	get testResult(): string {
		return this.listResult;
	}
	get testOk(): boolean | null {
		return this.listOk;
	}

	get aiBaseUrl(): string {
		return this.ai.current.value?.upstreams.find((upstream) => upstream.enabled)?.baseUrl ?? '';
	}

	get canUseAIUpstream(): boolean {
		const offered = this.aiBaseUrl;
		return offered !== '' && offered !== this.baseUrl.value;
	}

	save(): void {
		const body = buildSaveRequest(
			{
				name: this.name.value,
				baseUrl: this.baseUrl.value,
				model: this.model.value,
				apiKey: this.apiKey.value,
				defaultName: this.defaultName
			},
			false
		);
		void commands.saveEndpoint(this.contract, body);
		this.reset();
	}

	remove(): void {
		this.reset();
		void commands.removeEndpoint(this.contract);
	}

	async test(): Promise<void> {
		const base = this.baseUrl.value.trim();
		if (!base) return;
		this.listing = true;
		this.listResult = '';
		this.listOk = null;
		try {
			const res = await discoverEndpointModels(this.contract, base, this.apiKey.value || undefined);
			if (res.ok && res.models) {
				this.probed = res.models;
				this.listOk = true;
				const count = res.models.length;
				this.listResult = `Reached it. ${count} model${count === 1 ? '' : 's'} available.`;
			} else {
				this.listOk = false;
				this.listResult = res.error ?? 'Could not reach that endpoint.';
			}
		} catch (e) {
			this.listOk = false;
			this.listResult = e instanceof Error ? e.message : 'Listing models failed';
		} finally {
			this.listing = false;
		}
	}

	useAIUpstream(): void {
		const offered = this.aiBaseUrl;
		if (offered) this.baseUrl.value = offered;
	}

	private reset(): void {
		this.name.reset();
		this.baseUrl.reset();
		this.model.reset();
		this.apiKey.reset();
	}
}

export function useEndpoint(contract: EndpointContract, defaultName: string): EndpointForm {
	onMount(() => {
		void commands.loadEndpoint(contract);
	});
	return new EndpointState(
		select((state) => endpointView(state, contract)),
		select(aiView),
		contract,
		defaultName
	);
}
