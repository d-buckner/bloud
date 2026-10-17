// SPDX-License-Identifier: AGPL-3.0-only
//
// The settings round trip: save each setting back as it already is, and prove the
// orchestrator agrees with its own store.
//
//   node harness/bin/browser.mjs run settings-roundtrip
//
// This is the cheapest end-to-end test of the intent path that exists. A save is
// an intent, so the write lands on a queue and the read-back can still show the
// previous value. Each helper waits for the value to converge, which is what makes
// a mismatch mean something: either the intent never ran, or it applied something
// other than what was sent. Saving a value unchanged is also the safe write: the
// orchestrator's own no-op guard means nothing is recreated.

export const name = 'settings round trip';
export const description = 'save the address and the clock back unchanged, and prove both converge';
export const options = { open: false, timeoutMs: 5 * 60_000 };

export default async function ({ api, check, note }) {
  const health = await api.getHealth();
  check.eq(health.status, 'ok', 'the instance reports health before the round trip');

  const address = await api.getPublicURL();
  note(`the address on file is ${address.url}`);
  const savedAddress = await api.savePublicURLAndWait(address.url);
  check.eq(savedAddress.url, address.url, 'the address survives a save that changes nothing');

  const zone = await api.getTimezone();
  note(`the clock on file is "${zone.zone || '(unset)'}"`);
  const savedZone = await api.saveTimezoneAndWait(zone.zone);
  check.eq(savedZone.zone, zone.zone, 'the clock survives a save that changes nothing');

  const transcription = await api.getTranscriptionSettings();
  if (transcription.configured) {
    const saved = await api.setTranscriptionSettings({
      baseUrl: transcription.baseUrl,
      name: transcription.name,
      model: transcription.model,
    });
    check.eq(saved.baseUrl, transcription.baseUrl, 'the transcription endpoint survives a save that changes nothing');
  } else {
    check.skip(
      'the transcription endpoint survives a save that changes nothing',
      'no endpoint is configured, so there is nothing to round trip',
    );
  }

  const after = await api.getHealth();
  check.eq(after.status, 'ok', 'the instance still reports health after the round trip');
  const setup = await api.getSetupStatus();
  check.ok(setup.authentikReady, 'the identity provider still answers after the round trip');
}
