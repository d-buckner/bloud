# Transcription: the endpoint the instance declares

Some apps turn speech into text and have no opinion about where the model
lives. They call an OpenAI-compatible `/v1/audio/transcriptions` endpoint and
need three things: an address, a key, and optionally a model id.

Bloud holds those as one instance-level setting and hands them to whichever
apps ask. This is the `transcription` contract.

## Why a contract rather than an environment variable

The obvious implementation is a `BLOUD_WHISPER_*` variable forwarded into every
container that wants it. That breaks on three things this repository already
treats as settled:

- **The credential has to live in the secret store.** An env var copied into a
  container spec is visible in `podman inspect` and in every backup of the
  container definition. The contract resolves the key at reconciliation time
  and hands it to the configurator, which decides where it goes.
- **A change has to propagate.** When the operator edits the endpoint, every
  consumer has to be reconfigured. That is what the orchestrator's reset is
  for, and it only knows about things the graph can see. A variable has no
  graph.
- **Not every app wants it.** An app that transcribes declares that; an app
  that does not, does not. A global variable is a global opinion.

## The shape

```yaml
# apps/podly/metadata.yaml
integrations:
  transcription:
    required: false
    requires:
      - apiKey
    compatible:
      - source: setting
```

`source: setting` is the part that makes this different from every other
contract. The provider is not a catalog app: it has no container, no node, and
no graph edge, because the thing that serves it is a row in a settings table
that the operator filled in. The binding still arrives in
`state.Integrations.Transcription`, with `Installed` meaning "the operator
filled the role in" rather than "a container is running".

The contract is defined once, in
`services/host-agent/internal/catalog/contracts.go`: one secret (`apiKey`) and
one optional value (`model`). A consumer that declares `requires: [apiKey]`
gets the key; one that declares the contract without it gets the address and
nothing else, which is invariant 15's least-privilege rule applied to a
credential a human typed.

## Settings

Settings → Transcription holds one endpoint: a base URL (path included, so
`https://api.openai.com/v1` and `https://groq.com/v1` are both written exactly
as an SDK would want them), a display name, a model id, and a key.

| Endpoint | What it does |
|---|---|
| `GET /api/settings/transcription` | The stored endpoint, with the key masked |
| `PUT /api/settings/transcription` | Saves it and submits a `SetTranscriptionIntent` |
| `POST /api/settings/transcription/models` | Dials a candidate endpoint's `/models` so the model list can be picked rather than typed |

An empty `apiKey` on a save means "leave the stored key alone"; an explicit
empty string with a new URL means "this endpoint needs no key". Clearing the
endpoint removes the record and its credential together, so a key is never
left behind under a record nothing points at.

Saving is not writing. The API submits an intent and the orchestrator applies
it, because the orchestrator is the single writer. The apply is guarded on the
canonical form of the endpoint, so a save that only changed capitalisation, a
trailing slash, or whitespace does not reset every app that consumes the
contract.

## What a consumer does with it

The contract promises that whatever answers `Endpoint` transcribes. It does
not promise the app can be told about it, and that is the part each app's
configurator owns.

Podly is the reference consumer, and its case is instructive: the app has no
local model in the image Bloud ships, it accepts a remote endpoint through its
own `PUT /api/config`, and its config API returns the stored key as a preview
of the first four and last four characters. So Bloud cannot ask the app whether
it already holds the key it is about to send. It keeps a digest of what it last
delivered and writes only when that digest changes. See
[`apps/podly/INTEGRATION.md`](../../apps/podly/INTEGRATION.md).

Two rules fall out of that and apply to any future consumer:

- **No binding means no write, not a reset.** An operator who configured the
  app directly before Bloud had an opinion keeps it. A pass that cleared a
  working configuration because nothing happened to be wired in Bloud would be
  a pass that broke an install.
- **Gate the write on what Bloud applied, not on what the app holds.** The app
  cannot answer the interesting question about a credential it masks, and a
  consumer that re-delivered on every resync would overwrite a human's edit on
  a timer.

## Why this is not the `inference` contract

They look alike: both are OpenAI-compatible, both are operator-declared, both
carry an endpoint and a key. They are separate contracts because an endpoint
that completes text is not an endpoint that transcribes audio. `/v1/chat/
completions` and `/v1/audio/transcriptions` are different surfaces, and a
provider that serves one routinely does not serve the other. A single contract
would mean every consumer of "AI" had to guess which half it was talking to,
which is the same mistake as one contract for "HTTP API".

The `inference` contract also carries model lists and a gateway path because
Bloud can enumerate what a chat provider offers. Transcription keeps its model
as a plain id for the same reason inference's default model does: the server
behind the address has no Bloud-side name translation in front of it.
