// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The instance setting hooks: the address and the clock.
 *
 * Both saves restart every app, so both carry the consent gate. A component that
 * calls one of these gets the value, the consent, the read state and the save,
 * and needs to know nothing about the store, the draft, or the command.
 */

import { onMount } from 'svelte';
import { browserZone, offsetLabel, supportedZones, zoneOptions } from '$lib/utils/timezone';
import { select } from '../bindings.svelte';
import { commands } from '../commands';
import { sameText } from '../draft.svelte';
import { guarded } from '../field.svelte';
import type { GuardedSetting } from '../field.svelte';
import { publicUrlView, timezoneView } from '../selectors';

/** The public address, with the consent gate its restart requires. */
export function usePublicUrl(): GuardedSetting {
	const view = select(publicUrlView);
	onMount(() => {
		void commands.loadPublicURL();
	});

	return guarded({
		view: () => view.current,
		live: () => view.current.value ?? '',
		same: sameText,
		save: (value) => {
			void commands.savePublicURL(value);
		}
	});
}

/** A time zone, with the consent gate and the browser's zone list. */
export interface TimezoneSetting extends GuardedSetting {
	/** Zones the browser knows, plus every zone already in play. */
	readonly options: string[];
	/** The zone this browser sits in, or empty when it cannot tell. */
	readonly localZone: string;
	/** The offset of the selected zone, as a sentence fragment. */
	readonly offset: string;
	/** A one-click fill for the local zone, or empty when there is nothing to offer. */
	readonly suggestion: string;
	useSuggestion(): void;
}

/**
 * The zone list is platform knowledge, not form knowledge.
 *
 * Only the browser knows which zones exist without shipping a copy of the zone
 * database, and only the platform should know that. See `$lib/utils/timezone`
 * for the fallback and the reason the list may be short.
 */
class TimezoneState implements TimezoneSetting {
	private readonly zones = supportedZones();
	private readonly local = browserZone();

	constructor(private readonly base: GuardedSetting) {}

	get edit(): string {
		return this.base.edit;
	}
	set edit(next: string) {
		this.base.edit = next;
	}
	get dirty(): boolean {
		return this.base.dirty;
	}
	get confirmed(): string | null {
		return this.base.confirmed;
	}
	get pending(): boolean {
		return this.base.pending;
	}
	get loading(): boolean {
		return this.base.loading;
	}
	get error(): string {
		return this.base.error;
	}
	get acknowledged(): boolean {
		return this.base.acknowledged;
	}
	set acknowledged(on: boolean) {
		this.base.acknowledged = on;
	}
	get blocked(): boolean {
		return this.base.blocked;
	}
	get localZone(): string {
		return this.local;
	}

	get options(): string[] {
		// A saved zone the browser does not list still has to be selectable, or the
		// select would show the first option and a Save would change the zone the
		// operator did not touch.
		return zoneOptions(this.zones, [this.base.edit, this.base.confirmed ?? '', this.local]);
	}

	get offset(): string {
		return offsetLabel(this.base.edit);
	}

	get suggestion(): string {
		if (this.base.confirmed || !this.local || this.base.edit === this.local) return '';
		return this.local;
	}

	save(): void {
		this.base.save();
	}
	reset(): void {
		this.base.reset();
	}
	useSuggestion(): void {
		if (this.suggestion) this.base.edit = this.suggestion;
	}
}

export function useTimezone(): TimezoneSetting {
	const view = select(timezoneView);
	onMount(() => {
		void commands.loadTimezone();
	});

	return new TimezoneState(
		guarded({
			view: () => view.current,
			live: () => view.current.value ?? '',
			same: sameText,
			save: (zone) => {
				void commands.saveTimezone(zone);
			}
		})
	);
}
