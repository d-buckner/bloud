> Status: draft

# Plan: Inference settings, and why they do not need "logical apps"

## Problem

An operator runs an OpenAI-compatible server somewhere on their network (or
holds an API key for a hosted one). Several catalog apps can use it: Hermes today,
and anything agent-shaped that lands later. Right now each of those apps has to be
configured by hand, inside the app, with the same base URL and the same key. That
is exactly the integration knowledge Bloud exists to hold.

The ask is a Settings section: point Bloud at the OpenAI-compatible endpoint, and
every app that can use inference gets it.

That ask collides with one assumption in the architecture. Invariant 15 makes the
**installed app** the unit of integration: a provider is an app that declares
`provides:`, a consumer declares `integrations:`, and the orchestrator resolves
one into the other. The operator's external inference server is not an installed
app. It has no container, no lifecycle, no node in the graph.

The tempting answer is a new kind of catalog entry: an app with no containers, a
**logical app**. This plan argues against it, and proposes the smaller thing that
actually solves the problem.

## Decision

Three changes, all inside the existing contract machinery:

1. **Split the vocabulary into two contracts by role.** `modelSource` is any
   OpenAI-compatible upstream that something can route to. `inference` is the
   served endpoint an application dials: a base URL, a key, a default model.
2. **Add a second provider *source*.** A contract provider is normally an
   installed app. It can now also be **the instance**: a value the operator
   typed into Settings. The instance is a provider of `modelSource`.
3. **Make the instance's default model propagate.** One stored default flows to
   every consumer through the contract, under a single rule: adopt unless
   overridden.

```
Settings -> AI  (external OpenAI-compatible: base URL, key, models,
        |        default model)
        |        provider source: instance,  contract: modelSource
        |
        +--> modelSource --> litellm (app) --inference--> hermes, ...
        |                        ^                          |
        |                        |                    adopts the default
        |                        |                    unless it already
   ollama (app) --modelSource ---+                    picked its own
```

The load-bearing property is the last hop. A consumer declares `inference` and
never learns what is behind it. Adding LiteLLM to a running install that already
had Hermes pointed at a raw endpoint changes nothing in Hermes' metadata, its
config, or the operator's Settings entry. The gateway appears between them and
neither end moves.

## Facts this design rests on

Verified against the tree and the pinned image at the time of writing.

- **Hermes needs no plugin authoring, but env vars are not enough.** The pinned
  `nousresearch/hermes-agent:v2026.9.14` ships a `custom` provider profile
  documented as "any endpoint registered as `provider="custom"` (Ollama, vLLM,
  llama.cpp, ...)" and a plugin-based provider registry with per-install
  overrides at `$HERMES_HOME/plugins/model-providers/<name>/`, so a private
  profile is available later and never needs to be built. But `config.yaml` outranks
  the environment for provider selection, which is what
  [How Hermes must be registered](#how-hermes-must-be-registered) works through.
- **A containerless app parses today and then does nothing.** `Containers` is
  `yaml:"containers,omitempty"` (`internal/catalog/models.go`), so the loader
  accepts an app with no containers. The planner builds one graph node per
  container entry, so such an app has zero nodes: no lifecycle phases, no health
  check, no install progress, no dashboard tile.
- **`ProviderRef` is container-shaped.** `Node` is documented as "the provider's
  primary graph node, which is also its container name on the shared network",
  and `BaseURL` is `http://<Node>:<Port>`. This is the field set that a
  non-container provider cannot fill.
- **`resolveProviders` returns app IDs.** It reads `choice` plus
  `integration.Compatible[].App` (the latter only when the integration is not
  required), and each ID is then looked up with `catalog.Get`. A provider that is
  not a catalog app has no way through this function today.
- **`publishedSecret` resolves one secret per contract**, from the *provider's*
  app-secret scope: `secrets.GetAppSecret(providerID, spec.Secrets[0])`. That
  detail decides the shape of the gateway key question below.

## The contracts

Registry entries in `internal/catalog/contracts.go`:

```go
// An OpenAI-compatible upstream that something else can route to: the
// operator's own server (provided by the instance) or a local model runtime
// (provided by an app such as Ollama). A gateway consumes this; an
// application does not.
{
    Name:    "modelSource",
    Secrets: []string{"apiKey"},
    Values:  []ValueSpec{{Key: "models"}},
},

// The endpoint an application dials. Base URL, a key, a default model.
// Provided by a gateway app (LiteLLM) and, by promotion, by any modelSource
// when no gateway is installed.
{
    Name:         "inference",
    Secrets:      []string{"apiKey"},
    Values:       []ValueSpec{{Key: "models"}, {Key: "defaultModel"}},
    SatisfiedBy:  []string{"modelSource"},
},
```

`SatisfiedBy` is the new field on `Contract`: the promotion rule, declared in the
registry rather than buried in the orchestrator. When a consumer asks for
`inference` and no installed app provides it, the resolver falls back to the
listed contracts. A rule that lives in metadata stays visible to the person
reading the YAML.

The consumer binding:

```go
// InferenceBinding is the OpenAI-compatible endpoint an app dials.
type InferenceBinding struct {
    ProviderRef
    // Endpoint is the OpenAI-compatible base URL exactly as a client should
    // pass it to an SDK, path included: http://apps-litellm:4000/v1.
    Endpoint string
    // APIKey is the credential to send. Empty when the provider needs none
    // (a local Ollama on a trusted network).
    APIKey string
    // DefaultModel is the model id to use when the app has picked none.
    DefaultModel string
    // Models is the catalog on offer, for an app that renders a picker.
    Models []string
    // ViaGateway reports whether Endpoint is a Bloud-side gateway rather
    // than the raw upstream. A configurator uses it to decide whether the
    // credential it holds is a gateway key or the operator's real one.
    ViaGateway bool
}
```

`Endpoint` rather than the inherited `BaseURL` is deliberate. `BaseURL` is
documented as `http://<Node>:<Port>`, which is a container-network fact. The
OpenAI-compatible base URL carries a path (`/v1`) and, for an instance provider,
an origin that is not on any Bloud network. Naming the field after what it is
keeps the two from being conflated.

## Provider sources: the `ProviderRef` change

`CompatibleApp` gains a source discriminator:

```go
type CompatibleApp struct {
    App      string `yaml:"app,omitempty" json:"app,omitempty"`
    Source string   `yaml:"source,omitempty" json:"source,omitempty"`
    Default bool    `yaml:"default,omitempty" json:"default,omitempty"`
    Category string `yaml:"category,omitempty" json:"category,omitempty"`
}
```

Exactly one of `App` and `Source` must be set; the catalog loader rejects both or
neither. `source: instance` means the provider is the instance settings.

```yaml
integrations:
  inference:
    required: false
    multi: false
    compatible:
      - app: litellm
        default: true
      - source: instance
```

`ProviderRef` gains `Kind` (`"app"` | `"instance"`). For an instance provider:

| Field | Value |
|---|---|
| `App` | `"instance"` (reserved; not a catalog ID) |
| `Kind` | `instance` |
| `Installed` | the setting is populated and parses |
| `Node` | empty |
| `Port` | empty |
| `BaseURL` | empty (no container-network address exists) |
| `LocalURL` | the configured origin |

Two honest deviations from invariant 15, which says `Installed` "is the same
condition as the graph edge":

1. For an instance provider there is no graph edge. `Installed` means "the
   setting is populated". The invariant's *purpose*, which is that a consumer
   never probes a port to decide whether a provider exists, still holds: the
   answer comes from the store, not from a probe.
2. `Node` and `Port` are empty. Any consumer code that assumes them is
   consumer-side bug; `Endpoint` is the field that always carries a usable value.

**The discipline that keeps the graph clean:** `computeAppDeps` and every graph
edge builder must filter to `Kind == app`. An instance provider never produces a
node, never enters a convergence level, and never appears in the dashboard graph.
That filter is the single thing standing between this design and a graph full of
phantom nodes.

## Settings: the interface

One upstream now, shaped so N later is not a migration. The store holds a JSON
list even when it contains one entry.

| Key | Where | Contents |
|---|---|---|
| `settings['ai_upstreams']` | settings KV | JSON list of `{id, name, baseUrl, models, enabled}` |
| `settings['ai_default_model']` | settings KV | one concrete model id: the instance default every consumer adopts unless it has chosen its own |
| `ai/<upstreamId>/apiKey` | secrets manager | the upstream credential, never in the settings row |

The default is **instance-level, not per-upstream.** One key means one answer to
"what model does a freshly wired app get", which is the question the propagation
rule below has to answer unambiguously. A per-upstream default would require a
second rule to pick a winner among upstreams, and that rule would be pure
complexity while there is one upstream.

The key goes in the secrets manager, not the settings table. A settings row is
read by the UI on every page load and echoed into API responses; a credential
belongs behind `GetAppSecret`, which is already the thing that keeps secrets out
of catalog metadata and out of rendered config.

API, on the same module and auth as the existing settings routes:

| Route | Shape |
|---|---|
| `GET /api/settings/ai` | `{ upstreams: [{id, name, baseUrl, models, enabled, hasApiKey}], defaultModel: string, servedTo: [{app, via, model}] }` |
| `PUT /api/settings/ai` | `{ upstreams: [...], defaultModel: string }`; `apiKey` accepted, omitted means "keep", explicit empty means "clear" |
| `POST /api/settings/ai/test` | `{ baseUrl, apiKey }` in, `{ ok, models, error }` out. Operator-initiated, see below |

`PUT` submits a `SetInferenceIntent` through the orchestrator and returns 202
with the canonical rendering, the same contract `PUT /api/settings/hosts` uses.
The no-op guard compares the canonical form, so a save that changed nothing does
not restart every wired app.

`ParseInferenceBaseURL` is the gate on what may become an upstream. It reuses the
`ParsePublicURL` discipline (reject a query, a fragment, credentials, an unknown
scheme, an out-of-range port) with one deliberate difference: **a path is
required, not forbidden.** `/v1` is part of what makes an endpoint
OpenAI-compatible, and the operator types it. A bare origin with no path should
be accepted with a warning rather than silently rewritten, because some servers
do serve at the root and some do not, and guessing wrong produces a 404 that is
expensive to diagnose.

The `POST .../test` route is a probe, and it is allowed. Invariant 15 forbids the
*reconciler* from probing a port to decide whether a provider is installed,
because a probe cannot separate "not installed" from "restarting" and a prune
keyed off it deletes wiring that is still wanted. A button the operator presses
to check their own typo has none of that hazard: nothing is pruned from its
result, and it is not consulted during convergence.

UI: a new **AI** section in Settings, alongside Address, Tailnet, and Users. Base
URL, an API key field that is write-only and reports `hasApiKey`, a default model
selected from the discovered list, and a read-only "Served to" line naming each
wired app, whether it reaches the upstream directly or through a gateway, and
which model it is currently wired for.

## Default model propagation

The instance default answers one question: what model does a freshly wired
consumer get.

**It is a concrete model id, not a tier or an alias.** It has to resolve in
every topology this plan supports, and the promotion path is the strict test.
Pointed directly at the external server, through a LiteLLM gateway, or promoted
from a bare Ollama, a real model id is recognized by all three. A logical alias
like `bloud-default` 400s the moment it reaches a server that has never heard of
it, and every no-gateway path is exactly such a server. Tiers are a real idea,
but they are a gateway feature; see Non-goals.

**Adopt unless overridden.** The Hermes default-model policy is not a Hermes
rule, it is the consumer rule for this contract, stated once:

> A consumer adopts Bloud's default model only where it has not chosen its own.

Each configurator applies it to its own app's shape. The consequence is that the
feature never competes with the app: Bloud configures a fresh install completely
and never argues with a configured one. A consumer that exposes its own model
picker keeps working, and Bloud's setting stays what decides the *starting*
point rather than the running state.

**Propagation is a convergence pass, not a push.** Changing `ai_default_model`
submits a `SetInferenceIntent`. The orchestrator re-runs `PreStart` for every
app declaring the `inference` contract, each configurator rewrites its managed
keys under the adopt-unless-overridden rule, and the returned `changed` flag
decides whether anything restarts. Nothing reaches into a running app to swap a
model out from under a live session.

**Bloud does not model upstream model validity.** There is no staleness check on
the stored default, and that is deliberate rather than unfinished. A check would
compute a flag that changes nothing: the resolution is "keep serving the stored
value", so the flag is purely decorative. Worse, to be correct it needs a cached
copy of the discovered list kept fresh, which introduces a second staleness
problem in order to report the first. And it duplicates a signal the consumer
already delivers better, at the moment it matters: if the model is gone, the
app's own request fails and the operator sees it where they were trying to do
something.

This is the same principle as invariant 15's "never probe to decide whether a
provider is installed", applied one level down. Bloud does not hold state about
things it does not own. The picker being the live list means the value is valid at
save time; after that the upstream owns the fact, and the consumer's request is
the check.

**The picker is the live list.** The operator selects the default from discovered
models rather than typing an id, which removes the typo class entirely. When the
list cannot be fetched the field degrades to free text, and a stored value that is
not in the current list renders as its own entry rather than being dropped,
because an unreachable upstream must not block entering a correct value or eat
the one already saved.

## App sketch: Ollama

A real app with a real container. Nothing about it is logical.

```yaml
name: ollama
category: infrastructure
port: 11434
provides:
  modelSource:
    values:
      models: ""       # live catalog, fetched from /v1/models
containers:
  - name: apps-ollama
    image: docker.io/ollama/ollama:<pinned>
    restartPolicy: always
```

Ollama serves an OpenAI-compatible `/v1` natively and needs no credential on a
trusted network, so `modelSource` carries no required secret for it. The
`apiKey` slot stays in the contract because the *instance* provider needs it.

Open question, not blocking the contract: model pulls. `ollama pull llama3` is a
multi-gigabyte, user-initiated download. Whether Bloud drives it from the
dashboard or leaves it inside the app is a product decision, and it does not
change the contract shape.

## App sketch: LiteLLM

The gateway. Also a real app.

```yaml
name: litellm
category: infrastructure
port: 4000
integrations:
  modelSource:
    required: false
    multi: true
    compatible:
      - app: ollama
      - source: instance
provides:
  inference:
    secrets:
      - apiKey
    values:
      models: ""
      defaultModel: ""
containers:
  - name: apps-litellm
    image: docker.io/bitnami/litellm:<pinned>
    dependsOn: [apps-litellm-db]
  - name: apps-litellm-db
    image: docker.io/library/postgres:<pinned>
```

Postgres is its own container per invariant 3: LiteLLM needs durable storage for
virtual keys and usage, and it owns that infrastructure rather than sharing one.

The configurator is where the chain closes. `PreStart` renders
`litellm_config.yaml` from the resolved `modelSource` bindings: one LiteLLM
model group per upstream, model aliases merged, the instance upstream's key read
from the secrets manager. `PostStart` mints the gateway key through LiteLLM's
admin API and publishes it with `SetAppSecret("litellm", "apiKey")`, which is
what every `inference` consumer then receives.

**The per-consumer key wrinkle.** `publishedSecret` resolves a contract secret
from the *provider's* scope, so one LiteLLM key is shared by every consumer.
That is fine for v1 and it is a real weakening: no per-app rate limit, no
per-app usage attribution, and revoking access for one app means rotating the key
for all of them. Per-consumer virtual keys are a contract change, not a config
change: a `perConsumer` marker on the secret spec, resolved from the consumer's
own app-secret scope, minted by the provider during `PostStart` for each
declared consumer. Deferred, but the binding already carries `ViaGateway`, so the
information a later fix needs is present.

## Consumer wiring: Hermes

```yaml
integrations:
  inference:
    required: false
    multi: false
    compatible:
      - app: litellm
        default: true
      - source: instance
```

`PreStart` registers the binding in Hermes' `config.yaml` and, when no binding
resolves, strips the managed keys, the same way the SSO keys are stripped when
Authentik goes away, so a stale endpoint never outlives its setting.

### How Hermes must be registered

Inspected in the pinned image rather than assumed. Hermes has three ways to express
"use this OpenAI-compatible endpoint" and they are not equivalent.

**Env only is not reliable.** `OPENAI_BASE_URL` is read by the OpenAI SDK itself,
so it does take effect, but Hermes treats it as a lower-authority value than
`config.yaml`. `agent/auxiliary_client.py:4147` is a function whose entire job is
warning that the two disagree:

> "OPENAI_BASE_URL is set (%s) but model.provider is '%s'. Auxiliary clients may
> route to the wrong endpoint. Run: hermes model to reconfigure, or remove
> OPENAI_BASE_URL from ~/.hermes/.env"

So if the operator ever ran `hermes model` and picked a hosted provider, a
Bloud-set `OPENAI_BASE_URL` is read as exactly the stale `.env` poison that
function exists to catch. Env-only is the least trustworthy of the three options
precisely because it looks like the easiest one.

**Bare `custom` is single-slot and gated.** `model.provider: custom` plus
`model.base_url` works, but it is one global slot that collides with whatever the
operator chose in `hermes model`, and it cannot coexist with the user's own
custom providers. It also carries an upstream security control Bloud has to
satisfy deliberately. `hermes_cli/runtime_provider.py:66`,
`_config_base_url_trustworthy_for_bare_custom`, from upstream issue #14676:

> Whether `model.base_url` may back bare `custom` runtime resolution. The picker
can select Custom while `model.provider` still names a previous provider, so
non-loopback URLs are rejected unless the YAML provider is already `custom` or a
local-server alias (ollama/vllm/llamacpp): else a legit LAN ollama endpoint
falls through to OpenRouter. A stale OpenRouter/Z.ai base_url cannot hijack local
sessions.

That is a real anti-hijack rule, and it means Bloud must write **both** keys
consistently. Writing `model.base_url` alone does not make bare `custom` use it.

**Named provider entries are the intended shape.** Hermes v12+ keeps a `providers:`
map, surfaced through `get_compatible_custom_providers()` alongside the legacy
`custom_providers:` list, and a named entry is selected as `custom:<key>`. The
accepted key set (`hermes_cli/config_providers.py:113`) is generous:

```
name, api, url, base_url, api_key, key_env, api_key_env, key_cmd, api_mode,
transport, model, default_model, models, models_discovered, context_length,
rate_limit_delay, request_timeout_seconds, stale_timeout_seconds,
discover_models, extra_body, extra_headers, capabilities, ssl_ca_cert, ssl_verify
```

Unknown keys are warned about and ignored rather than fatal, which makes this a
forgiving merge target. Two fields matter to this plan:

- **`api_key`** is what the implementation writes. The alternative, `key_env`,
  names a container environment variable instead of putting the credential in
  `config.yaml`, and it is the better shape in principle. It is not what shipped,
  because the container spec is rendered from `metadata.yaml` with a fixed set of
  template variables (`{{dataDir}}`, `{{appDataDir}}`, `{{postgresPassword}}`,
  the LDAP outpost token) and there is no per-consumer resolved-integration
  variable in that set. Plumbing one means teaching spec building to run
  integration resolution, which it currently does not do. The exposure difference
  is also smaller than it looks: `key_env` moves the secret into
  `podman inspect` output, which is readable by anyone with the socket, while the
  config file sits in the app's own data directory, readable by anyone with that
  path. Both are host-level reads. Repaying this is a tracked follow-up, not a
  correctness problem.
- **`discover_models`** is Hermes doing live `/models` discovery itself. Given the
  decision that the model list is live rather than operator-typed, Bloud sets this
  to true and does not have to mirror a model list into Hermes at all. Bloud's own
  Settings still stores `ai_default_model` for the contract and for apps that
  cannot self-discover, but Hermes is not one of them.

The registration Bloud writes, per wired install:

```yaml
# config.yaml, Bloud-managed keys only. Everything else belongs to the user.
providers:
  bloud:
    name: Bloud
    api: openai-completions
    base_url: http://apps-litellm:4000/v1   # or the external origin, unwired
    api_key: <resolved from the secrets manager>   # omitted when the binding has none
    default_model: <settings ai_default_model>
    discover_models: true
model:
  provider: custom                # set only when the user has not chosen one
  model: bloud/<ai_default_model>
```

`bloud` is a reserved provider key: the configurator owns that map entry and
strips it when the setting goes away. A binding with no credential writes no
`api_key` at all, so a keyless endpoint never sends a blank bearer token.

**The default-model policy is the intrusive part.** Setting `model.provider` makes
Bloud's endpoint the model every Hermes session uses, which overrides a choice the
operator may have made in Hermes' own UI. The policy this plan adopts:

| `model.provider` state | Bloud action |
|---|---|
| unset | Set it to `custom` + `bloud/<default>`. A fresh install gets inference working immediately. |
| already pointing at `bloud/…` | Leave it. No churn. |
| something the user chose | Register the provider, do **not** override. The user selects it in Hermes. |

That is a one-line rule with a real consequence: Bloud configures a new install
completely and never fights a configured one. It also means the configurator reads
before it writes, which the existing Hermes SSO merge already does.

**Why this is a config merge and not env-only.** The `providers:` map and the
read-before-write default policy both require merging `config.yaml`. That is the
same machinery the SSO integration already uses, including the semantic
comparison that reports `changed=false` when the managed keys already match on
disk, so wiring inference in adds no restart churn beyond what SSO established.

## Why this keeps the doors open

| Install state | What a consumer receives |
|---|---|
| Settings only | the external server, directly, `ViaGateway: false` |
| Ollama only | Ollama, promoted from `modelSource`, `ViaGateway: false` |
| Settings + LiteLLM | the LiteLLM endpoint and gateway key, `ViaGateway: true` |
| Settings + Ollama + LiteLLM | one LiteLLM endpoint; LiteLLM routes per model alias |
| N external upstreams | N `modelSource` entries; still zero logical apps |
| Nothing configured | empty binding; the app runs unconfigured, as today |

Every row is the same consumer metadata. That is the test the design has to pass:
**no row requires an app to change what it declares.**

## What "logical apps" would have cost

An app with no containers is accepted by the loader and then breaks every
app-shaped expectation downstream:

| App-shaped thing | With a logical app |
|---|---|
| Install | nothing to pull; converges instantly with zero nodes, so the UI reports a success that installed nothing |
| Uninstall | nothing to remove, and the operator's setting is destroyed or orphaned by it |
| Health | undefined: there is no container to probe and no phase to reach |
| Graph | no node, so the provider is invisible in the view that exists to show integrations |
| Image pins | no image, so it needs an exemption in `scripts/pinned-images.mjs`, and an exemption table that exists to rot |
| `estimatedSizeMB` | meaningless |
| Lifecycle phases | `INITIALIZING → PRESTART → STARTING → POSTSTART → RUNNING` over an empty node set |

Each of those is a special case in the reconciler. The tech-debt ledger's
open items are disproportionately silent-failure paths in the engine. Adding six
more ways for the engine to have nothing to converge on is the wrong trade for
avoiding one `Kind` field.

The underlying category error: the operator's external server is not a thing you
install. It is a thing you configure. Putting it in the catalog makes the catalog
lie about what it contains.

## Invariant impacts

| Invariant | Impact |
|---|---|
| 1. Orchestrator is the single writer | Clean. The setting flows as `SetInferenceIntent`; the API handler submits and returns current state. |
| 3. Apps own their infrastructure | Clean, and reinforced. The instance provider owns nothing, which is precisely why it is not an app. |
| 4. The graph sees nodes, not apps | Requires the `Kind == app` filter in every edge builder. An instance provider must never create a node. |
| 15. Typed integration contracts | Extended with a second provider source. Two documented deviations, listed above. `bindContract` gains one arm per contract, which is the designed extension point. |

Invariant 15's rule "never add a field to a shared binding struct" is respected:
`InferenceBinding` is a new payload type, and `ProviderRef.Kind` is a
discriminator on the shared ref rather than a contract-specific field. If that
latter addition feels wrong in review, the alternative is a parallel
`InstanceRef` and a union binding, which is more code for the same information.

## Implementation phases

**Phase 0, prerequisite.** The settings KV refactor in flight in the primary
checkout (`store/settings.go`, `settings['public_url']`, the `hosts` table
removal) lands first. This plan stores `ai_upstreams` in that KV, and both
changes touch `settings_module.go`, `settingsClient.ts`, and
`settings/+page.svelte`. Landing them concurrently means a three-file conflict in
the exact files where the shape matters.

**Phase 1, the vertical slice.** Contract registry entries and the `SatisfiedBy`
field. `CompatibleApp.Source`, `ProviderRef.Kind`, the `computeAppDeps` filter,
the promotion in `buildIntegrations`. `ParseInferenceBaseURL`. Settings API and
UI section, including the live `GET <base>/models` read that backs the model
display and the `ai_default_model` picker. Hermes consumer:
the `providers.bloud` merge with `discover_models`, and the
read-before-write default policy. Result: Settings to a working Hermes with no
gateway app installed, wired to the model chosen in Settings.

**Phase 2.** Ollama as an app. Proves `modelSource` works with an app provider
and exercises promotion from an app rather than from Settings.

**Phase 3.** LiteLLM as an app. Proves the gateway chain and the promotion
suppression: a real `inference` provider wins over the `SatisfiedBy` fallback,
so installing the gateway moves every consumer onto it without the consumer
changing anything. The per-consumer key question is already decided (shared key
for v1); what this phase validates is that the decision is cheap to revisit,
because `ViaGateway` already distinguishes the two cases in the binding.

## Failure modes

| Condition | Result |
|---|---|
| Setting unset | Empty binding. Apps run unconfigured, exactly as today. |
| Base URL is not OpenAI-compatible | The `POST .../test` route reports it before save. At runtime the consumer gets a 404 or a 401 from its own client; Bloud does not second-guess the upstream. |
| Upstream key wrong | Same as above: surfaced by the test route, and by the consumer's own auth failure. |
| Both LiteLLM and the instance setting present | LiteLLM wins for `inference` (a real provider beats the promotion fallback). The instance is still consumed by LiteLLM as a `modelSource`. |
| LiteLLM uninstalled while Hermes is installed | Promotion re-runs on the next convergence and Hermes falls back to the raw upstream. No manual repair. |
| Upstream disappears mid-run | Bloud does not detect it. There is no health model for an external endpoint, and pretending otherwise would be the probe invariant 15 forbids. |
| Stored default model retired upstream | Bloud does not know and does not guess. The consumer's next request fails against the upstream and the operator sees it in the app, which is where they were trying to do something. |
| `/models` unreachable when Settings opens | Model list empty, picker degrades to free text, the stored default renders as its own entry. Nothing is cleared and nothing is blocked. |
| Operator changes the default model | `SetInferenceIntent` re-runs `PreStart` on every `inference` consumer. Consumers that had chosen their own model are untouched; consumers on the Bloud default move to the new one. |
| Consumer has its own model picker set | Adopt-unless-overridden: Bloud registers the provider but leaves the consumer's chosen model alone. |
| Operator sets a bare origin with no `/v1` | Accepted with a warning. The consumer's first request is where it shows. |

## Non-goals

- **Per-app model selection.** The binding carries a `Models` list so an app can
  render a picker, but Bloud does not store a per-app model choice. That is a
  product surface with no consumer yet.
- **Logical model tiers.** `fast` / `smart` / `default` mapped to real models is
  a good idea with a hard constraint: it only resolves through a gateway. A tier
  name sent to a server that has never heard of it is a 400, so tiers cannot be
  the contract's default shape. They are a natural LiteLLM model-group feature
  and belong to a later design, where `InferenceBinding.DefaultModel` carrying an
  alias instead of an id is a value change rather than a contract change.
- **Per-app model override in Bloud.** The adopt-unless-overridden rule already
  leaves a consumer's own choice alone; adding a Bloud-side per-app override on
  top would mean Bloud overriding the app's own picker, which is the exact
  behavior this design rejects.
- **Quotas, cost caps, rate limits.** Gateway features. They belong to LiteLLM's
  own UI once it exists as an app, not to Bloud's settings.
- **Embeddings, vision, audio, rerank.** All reachable through the same
  LiteLLM endpoint today. If they ever need distinct contracts, `SatisfiedBy`
  generalizes; do not pre-build for it.
- **TLS for the upstream.** Bloud sends whatever the operator configured. A
  plaintext `http://` upstream with a real API key is the operator's choice and
  should be visually flagged in the UI, not refused.
- **Multiple named AI "profiles" per app.** One upstream list, one served
  endpoint. Per-app routing beyond the default is LiteLLM's job.

## Verification

- **Default model propagation:** the stored default resolves into
  `InferenceBinding.DefaultModel` for every consumer; changing the default
  re-runs `PreStart` across all `inference` consumers; a consumer that already
  carries its own model is not overwritten. Assert there is no validity check:
  a default absent from the discovered list still resolves into the binding
  unchanged.
- **Contract registry:** `SatisfiedBy` resolves only to declared contracts; an
  unknown name fails the load. `modelSource` and `inference` validate their
  declared secrets and values.
- **Resolver:** promotion fires only when no `inference` provider is installed;
  a real provider suppresses the fallback; the promoted binding carries
  `ViaGateway: false`; uninstalling the gateway re-promotes on the next pass.
- **Graph:** an `inference` binding whose provider is `source: instance`
  produces no graph node and no edge. Assert this directly; it is the property
  the design depends on most.
- **Parser:** `ParseInferenceBaseURL` accepts a path, rejects a query,
  fragment, credentials, unknown scheme, out-of-range port.
- **Settings API:** the no-op guard catches a canonical-identical save; the API
  key never appears in a `GET` response; omitted versus empty means keep versus
  clear.
- **Hermes configurator:** binding present writes the `providers.bloud` entry
  with `discover_models: true`; the credential is written as `api_key` for v1
  (see the `key_env` note above for why the env indirection is deferred),
  binding absent strips the managed entry; a second pass over unchanged state
  reports `changed=false`. The default policy is asserted three ways: an unset
  `model.provider` becomes `custom` with `model: bloud/<default>`, an existing
  Bloud selection is left byte-identical, and a provider the user chose survives
  every pass untouched.
- **Live:** a stub OpenAI-compatible container (a few lines of Python serving
  `/v1/models` and `/v1/chat/completions`) as the instance upstream, so the
  whole path is testable without a real API key or network egress.
- **Regression:** `./bloud e2e lifecycle` with no AI setting configured must be
  byte-identical in behavior to today.

## Resolved decisions

- **Model list is live.** The operator does not type model ids. Bloud reads
  `<base>/models` for its own Settings display and for the contract's
  `defaultModel` (resolved from the instance's `ai_default_model`), and sets
  `discover_models: true` on Hermes so the app keeps its own live view rather
  than a snapshot Bloud pushed. A stored list would go stale the moment the
  upstream changes and would be Bloud's copy of a fact the upstream owns.
- **No `perConsumer` secret shape for v1.** One gateway key per provider, resolved
  from the provider's app-secret scope, as `publishedSecret` already works. The
  per-app isolation loss (no per-app rate limit, no usage attribution, revoking
  one app rotates all of them) is accepted for now. `ViaGateway` on the binding is
  the hook a later change needs.
- **Promotion handing the raw upstream credential to a consumer is acceptable to
  start.** With no gateway installed there is no alternative: the consumer cannot
  reach the upstream without the upstream's key. The credential-safety clause that
  a gateway setup implies (a consumer should never hold the operator's real key
  when a gateway exists) is enforced by the promotion rule's own condition, since
  promotion only fires when no `inference` provider is installed.

- **Bloud sets Hermes' default model only when nothing is chosen.** The policy
  table above is the decision, not a proposal: register `providers.bloud` on
  every wired install, set `model.provider: custom` with
  `model: bloud/<default>` only when the operator
  has not selected a provider, and never override a choice a human made. A fresh
  install works out of the box; a configured one is left alone. The cost is that
  the configurator must read before it writes, which the existing Hermes SSO merge
  already does.
- **The propagated default is a concrete model id.** A real id the upstream
  recognizes, resolvable direct, through a gateway, and via promotion. A logical
  alias cannot survive the no-gateway path, which is the path this plan is built
  to support.
- **Bloud does not model upstream model validity.** No staleness check, no
  `defaultModelStale` flag. A flag that changes nothing, needs a cache kept fresh
  to be correct, and duplicates a failure the consumer reports better and at the
  right moment. The consumer's own request is the check.
- **Adopt unless overridden is the consumer rule for the whole contract**, not a
  Hermes special case. Bloud sets the starting point and never fights a
  configured app.
- **Settings only, not first-run.** Inference is not required for a working
  install, so it stays out of the first-run wizard rather than lengthening a flow
  already carrying the address and admin-account decisions.

## Open questions

1. Ollama model pulls: Bloud-driven from the dashboard, or left to the user
   inside the app? Multi-gigabyte downloads make this a real UX decision, and it
   does not change the contract shape.
2. **Adopt-unless-overridden cannot tell "the operator chose this" from "Bloud
   chose this earlier."** The policy is implemented as written above: if
   `model.provider` is already set, the active model is left alone. But a fresh
   install has Bloud set it, so a later change to the instance default updates
   `providers.bloud.default_model` while `model.model` keeps the value Bloud
   wrote on day one. Changing the default therefore does not move a Hermes that
   Bloud itself configured, which is probably not what an operator expects.
   Fixing it means recording which fields Bloud wrote (a marker in the managed
   block, or a separate "bloud_adopted" key) so a later pass can tell its own
   earlier choice from a human's. That is a real design decision, not a bug to
   paper over.

## Tracked follow-ups

Found verifying this feature end to end against a live install:

- [#136](https://github.com/d-buckner/bloud/issues/136): Hermes takes ownership
  of `/opt/data` and locks host-agent out of the config file it manages, on the
  native backend. Blocks the Hermes consumer entirely there. Pre-existing: the
  failing read is the shared SSO merge path, unchanged from `main`.
- [#137](https://github.com/d-buckner/bloud/issues/137): no periodic
  self-healing reconcile pass. `resetInferenceConsumers` only resets nodes in
  `RUNNING`, so a consumer stuck in `error` never picks up a settings change.
  The proposed `ReconcileIntent` on a timer is what makes that case self-correct
  once the underlying cause is cleared.
