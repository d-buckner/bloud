// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Building the PATCH body for one external app.
 *
 * The rule that earns this its own module is the credential one. The list
 * endpoint never echoes a secret back, so a config form cannot resend the
 * credential it is only being asked to confirm. The server reads an *omitted*
 * contract as "keep the one on file" and a *present but blank* one as a
 * credential the operator typed, which it rejects. So blank secrets must be
 * dropped from the body rather than sent as empty strings, and that is exactly
 * the kind of rule a test should own.
 */

export interface ExternalAppForm {
	name: string;
	url: string;
	icon: string;
	values: Record<string, Record<string, string>>;
	secrets: Record<string, string>;
}

export interface ExternalAppPatch {
	name: string;
	url: string;
	icon?: string;
	values?: Record<string, Record<string, string>>;
	secrets?: Record<string, string>;
}

/**
 * The patch for a launcher: name, URL, and the optional icon. No credential
 * section, because a launcher wires to nothing.
 */
export function launcherPatch(form: ExternalAppForm): ExternalAppPatch {
	return {
		name: form.name.trim(),
		url: form.url.trim(),
		icon: form.icon.trim()
	};
}

/**
 * The patch for a remote install: name, endpoint, contract values, and only
 * the credentials the operator actually typed.
 *
 * A form where every secret is blank carries no `secrets` key at all. That is
 * the difference between "leave the stored credential alone" and "here is a
 * blank credential", and only the second one the server will refuse.
 */
export function providerPatch(form: ExternalAppForm): ExternalAppPatch {
	const supplied = Object.fromEntries(
		Object.entries(form.secrets).filter(([, value]) => value.trim() !== '')
	);
	const patch: ExternalAppPatch = {
		name: form.name.trim(),
		url: form.url.trim(),
		values: form.values
	};
	if (Object.keys(supplied).length > 0) {
		patch.secrets = supplied;
	}
	return patch;
}

/**
 * Whether one required contract field still needs input.
 *
 * A secret already on file counts as filled: the form cannot show a value it
 * was never given, so it should not have to invent one to close the dialog.
 */
function fieldIsFilled(
	field: { contract: string; key: string; kind: 'value' | 'secret' },
	form: ExternalAppForm,
	storedSecrets: string[]
): boolean {
	if (field.kind === 'value') {
		return (form.values[field.contract]?.[field.key] ?? '').trim() !== '';
	}
	return (form.secrets[field.contract] ?? '').trim() !== '' || storedSecrets.includes(field.contract);
}

/**
 * Whether a required field is still missing. A secret already on file counts as
 * satisfying its own requirement, because the form cannot show a value it was
 * never given and should not have to invent one to close the dialog.
 */
export function missingRequiredFields(
	form: ExternalAppForm,
	required: { contract: string; key: string; label: string; kind: 'value' | 'secret' }[],
	storedSecrets: string[]
): string[] {
	const missing: string[] = [];
	if (!form.name.trim()) missing.push('name');
	if (!form.url.trim()) missing.push('endpoint');
	for (const field of required) {
		if (!fieldIsFilled(field, form, storedSecrets)) {
			missing.push(field.label);
		}
	}
	return missing;
}
