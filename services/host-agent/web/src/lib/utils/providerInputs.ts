// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The operator inputs a remote app actually owes.
 *
 * The add and configure forms are generated from the provider's `provides:`
 * and the contract registry, and the registry is generous: it lists every
 * value a contract *can* carry. Generous is not the same as required, and a
 * form that renders the whole registry asks a person to type facts they do
 * not own. Three rules do the trimming:
 *
 * 1. A value the provider's own catalog entry declares statically is a fact
 *    about the app, not about this install. `/feed/v3/calendar/Radarr.ics`
 *    is true of a remote Radarr unchanged, so the form never shows it; the
 *    server merges it back in on save.
 * 2. Optional values stay out. An optional field with nothing to say reads
 *    as an empty binding either way, so the input only manufactures a blank.
 * 3. One credential answers every contract of one app that declares the same
 *    secret name. Radarr's `pvr` and `icsFeed` offers both publish `apiKey`:
 *    it is the same key, and asking twice only lets the two copies disagree.
 *    The server shares it the same way (`sharedSecret`), so asking once is
 *    not a relaxation of anything.
 *
 * What survives for a remote Sonarr or Radarr is the endpoint and one API
 * key. The contract grouping the registry implies is not something a person
 * needs in order to fill the form, so it does not appear either.
 */
import type { ExternalProviderContract, ExternalProviderField } from '$lib/clients/settingsClient';

/**
 * One field the operator fills, and the set of contracts that field answers.
 *
 * `contracts` is plural because of rule 3: a single input fans out to every
 * contract it covers, so the payload the form builds still carries a value
 * per contract, exactly as the resolver expects.
 */
export interface ProviderInput {
	/** Stable identity across renders: the kind and the registry key. */
	id: string;
	key: string;
	label: string;
	kind: 'value' | 'secret';
	contracts: string[];
	help: string;
}

/** What was typed, keyed by {@link ProviderInput.id}. */
export type ProviderFieldValues = Record<string, string>;

/** The per-contract payload the API expects. */
export interface ProviderPayload {
	values: Record<string, Record<string, string>>;
	secrets: Record<string, string>;
}

/**
 * The required inputs for one provider option, deduplicated across its
 * contracts and ordered by first appearance.
 *
 * Only `required` fields enter the list. A field the backend marks required
 * while carrying a static `default` cannot happen: the derivation that sets
 * `Required` clears it for anything the catalog already answers, so every
 * field that reaches here starts blank and stays that way until typed.
 */
export function deriveProviderInputs(contracts: ExternalProviderContract[]): ProviderInput[] {
	const byId = new Map<string, ProviderInput>();
	for (const contract of contracts ?? []) {
		for (const field of contract.fields ?? []) {
			if (!field.required) continue;
			mergeInput(byId, contract.name, field);
		}
	}
	return [...byId.values()];
}

/**
 * Add one field to the accumulator, folding it into an existing input when
 * the two carry the same kind and key.
 */
function mergeInput(
	byId: Map<string, ProviderInput>,
	contractName: string,
	field: ExternalProviderField
): void {
	const id = `${field.kind}:${field.key}`;
	const seen = byId.get(id);
	if (!seen) {
		byId.set(id, {
			id,
			key: field.key,
			label: field.label,
			kind: field.kind,
			contracts: [contractName],
			help: field.help ?? ''
		});
		return;
	}
	if (!seen.contracts.includes(contractName)) seen.contracts.push(contractName);
	if (!seen.help && field.help) seen.help = field.help;
}

/**
 * The value to show for one input when seeding a form from a stored record.
 *
 * A secret has none: the list endpoint never echoes a credential back, so a
 * password box always starts empty and the placeholder is what says whether
 * one is already on file.
 */
export function readProviderInput(input: ProviderInput, values: Record<string, Record<string, string>>): string {
	if (input.kind === 'secret') return '';
	for (const contract of input.contracts) {
		const value = values[contract]?.[input.key] ?? '';
		if (value.trim() !== '') return value;
	}
	return '';
}

/**
 * Fan the typed values out into the per-contract payload the API takes.
 *
 * A blank input writes nothing at all rather than an empty string. For a
 * credential that is the difference between "leave the one on file alone"
 * and "here is a blank one", and only the second the server refuses.
 */
export function buildProviderPayload(inputs: ProviderInput[], fieldValues: ProviderFieldValues): ProviderPayload {
	const values: Record<string, Record<string, string>> = {};
	const secrets: Record<string, string> = {};
	for (const input of inputs) {
		const value = (fieldValues[input.id] ?? '').trim();
		if (value === '') continue;
		if (input.kind === 'secret') {
			for (const contract of input.contracts) secrets[contract] = value;
			continue;
		}
		for (const contract of input.contracts) {
			values[contract] = { ...(values[contract] ?? {}), [input.key]: value };
		}
	}
	return { values, secrets };
}

/**
 * Labels of the inputs still missing, for the submit gate and its message.
 *
 * A secret counts as satisfied when any contract it covers already has one
 * stored, because the form cannot show a credential it was never given and
 * should not have to invent one to close the dialog.
 */
export function missingProviderInputs(
	inputs: ProviderInput[],
	fieldValues: ProviderFieldValues,
	storedSecrets: string[] = []
): string[] {
	return inputs
		.filter((input) => !isFilled(input, fieldValues, storedSecrets))
		.map((input) => input.label);
}

function isFilled(input: ProviderInput, fieldValues: ProviderFieldValues, storedSecrets: string[]): boolean {
	if ((fieldValues[input.id] ?? '').trim() !== '') return true;
	return input.kind === 'secret' && input.contracts.some((contract) => storedSecrets.includes(contract));
}
