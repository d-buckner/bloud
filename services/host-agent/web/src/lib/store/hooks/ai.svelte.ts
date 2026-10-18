// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The chat settings hook.
 *
 * One call gives the chat form its three fields, the write-only key, the stored
 * facts it displays, the probe that lists models, and the save. The payload
 * shape, the "omitted key means keep" rule, and the probe belong here, not in a
 * component that also has to render a select.
 */

import { onMount } from 'svelte';
import { testAIEndpoint } from '$lib/clients/settingsClient';
import type { AIServedTo, AISettings, AIUpstream, SetAIRequest } from '$lib/clients/settingsClient';
import { select } from '../bindings.svelte';
import type { Readable } from '../bindings.svelte';
import { commands } from '../commands';
import { sameText } from '../draft.svelte';
import { field, isLoading, readError } from '../field.svelte';
import type { Field } from '../field.svelte';
import { aiView } from '../selectors';
import type { ResourceView } from '../selectors';

/** The chat endpoint, its three fields, and the probe that lists its models. */
export interface AIForm {
	name: Field<string>;
	baseUrl: Field<string>;
	defaultModel: Field<string>;
	/** Write-only: the API never returns the key, so blank means "keep". */
	apiKey: Field<string>;
	readonly confirmed: AISettings | null;
	readonly hasApiKey: boolean;
	readonly servedTo: AIServedTo[];
	readonly dirty: boolean;
	readonly loading: boolean;
	readonly error: string;
	/** Models from the last probe, or the stored list before one runs. */
	readonly models: string[];
	readonly testing: boolean;
	readonly testResult: string;
	readonly testOk: boolean | null;
	save(): void;
	test(): void;
}

class AIState implements AIForm {
	readonly name: Field<string>;
	readonly baseUrl: Field<string>;
	readonly defaultModel: Field<string>;
	readonly apiKey: Field<string>;

	// The probe's answer belongs to the form, not to the store: it lists what a
	// candidate endpoint offers, and nothing else reads it.
	private probed = $state<string[]>([]);
	private probing = $state(false);
	private probeResult = $state('');
	private probeOk = $state<boolean | null>(null);

	constructor(private readonly view: Readable<ResourceView<AISettings>>) {
		// v1 is single-upstream; the shape is a list so adding a second later is not
		// a redesign. The form edits the first one.
		this.name = field({ live: () => this.upstream()?.name || 'Default', same: sameText });
		this.baseUrl = field({ live: () => this.upstream()?.baseUrl ?? '', same: sameText });
		this.defaultModel = field({ live: () => this.live()?.defaultModel ?? '', same: sameText });
		this.apiKey = field({ live: () => '' });
	}

	private live(): AISettings | null {
		return this.view.current.value;
	}

	private upstream(): AIUpstream | undefined {
		return this.live()?.upstreams[0];
	}

	get confirmed(): AISettings | null {
		return this.view.current.confirmed;
	}
	get hasApiKey(): boolean {
		return this.live()?.hasApiKey ?? false;
	}
	get servedTo(): AIServedTo[] {
		return this.view.current.confirmed?.servedTo ?? [];
	}
	get dirty(): boolean {
		return this.name.dirty || this.baseUrl.dirty || this.defaultModel.dirty || this.apiKey.dirty;
	}
	get loading(): boolean {
		return isLoading(this.view.current);
	}
	get error(): string {
		return readError(this.view.current);
	}
	get models(): string[] {
		return this.probed.length > 0 ? this.probed : (this.upstream()?.models ?? []);
	}
	get testing(): boolean {
		return this.probing;
	}
	get testResult(): string {
		return this.probeResult;
	}
	get testOk(): boolean | null {
		return this.probeOk;
	}

	save(): void {
		const candidate: AIUpstream = {
			id: this.upstream()?.id ?? 'default',
			name: this.name.value.trim() || 'Default',
			baseUrl: this.baseUrl.value.trim(),
			models: this.models,
			enabled: true
		};
		const payload: SetAIRequest = {
			upstreams: this.baseUrl.value.trim() ? [candidate] : [],
			defaultModel: this.defaultModel.value.trim()
		};
		// Only include the key when it was typed. Omitting means "keep".
		if (this.apiKey.value !== '') payload.apiKey = this.apiKey.value;

		void commands.saveAISettings(payload);
		this.reset();
	}

	async test(): Promise<void> {
		const base = this.baseUrl.value.trim();
		if (!base) return;
		this.probing = true;
		this.probeResult = '';
		this.probeOk = null;
		try {
			const res = await testAIEndpoint(base, this.apiKey.value || undefined);
			if (res.ok && res.models) {
				this.probed = res.models;
				this.probeOk = true;
				const count = res.models.length;
				this.probeResult = `Reached it. ${count} model${count === 1 ? '' : 's'} available.`;
			} else {
				this.probeOk = false;
				this.probeResult = res.error ?? 'Could not reach that endpoint.';
			}
		} catch (e) {
			this.probeOk = false;
			this.probeResult = e instanceof Error ? e.message : 'Test failed';
		} finally {
			this.probing = false;
		}
	}

	private reset(): void {
		this.name.reset();
		this.baseUrl.reset();
		this.defaultModel.reset();
		this.apiKey.reset();
	}
}

export function useAI(): AIForm {
	const view = select(aiView);
	onMount(() => {
		void commands.loadAISettings();
	});
	return new AIState(view);
}
