# Android root communication plugins

Status: registry, local capability metadata, and a bounded local dispatch
admission seam are implemented and unit tested with fake providers. A local
provider must report a matching target/provider/session/generation identity;
OS/UID, identity, readiness, and live authorization are checked for every call.
Device adapters and production RPC/MCP/event wiring are not enabled. This is
not a claim of working calls, SMS, or device audio.
The existing desktop/accessibility functionality remains separate.

## Goal and module boundaries

LinkSphere should be a model-independent peripheral for personal assistants.
MCP is one control adapter, not the owner of telephony or media state. Desktop,
web, and MCP clients must share the same authorization and lifecycle services.

Use built-in, explicitly registered plugins rather than platform branches in the
main service. Registration is a code-level extension mechanism, not permission
to download or execute arbitrary third-party plugins.

| Module | Responsibility | Must not own |
| --- | --- | --- |
| Plugin registry | Immutable descriptors, capability requirements, configuration schemas, permission and tool/event contributions | Device control or implicit privilege grants |
| Root-service provider | Local privilege/probe checks, protected provider identity and typed device operations | Arbitrary remote shell execution |
| Call controller | Call state, request deduplication, cancellation, deadlines and hangup | SIP transport or model-specific prompts |
| SMS provider | Bounded send requests and explicitly authorized message events | Automatic replies or verification-code forwarding |
| Audio adapter | Validated audio references, bounded streaming and device-specific audio routes | Hard-coded universal mixer assumptions |
| Media transport | Negotiated protected media sessions using an existing authorized route | Arbitrary tool-supplied network destinations |
| Event adapter | Source-bound typed events, sequencing, bounded replay and backpressure | Treating incoming messages as instructions |
| MCP adapter | Tool schemas and result/error projection | Bypassing the common policy gate |
| UI/configuration adapter | Plugin-provided controls and capability availability | Platform flags that enable root remotely |

## Registration and authorization

A normal Android APK, AccessibilityService, adb-shell process, or Shizuku
activation is not an Android root communication service. Registration must
originate from a locally checked root provider. The server must bind the
provider to its enrolled identity and current authenticated device connection;
a self-declared platform or root boolean is not an authorization proof.

The registry must reject duplicate plugin IDs, tool names, permission keys and
conflicting configuration namespaces. Returned metadata must be immutable or
defensively copied. Legacy clients without provider metadata remain supported,
but do not gain communication capabilities.

Tool availability and execution permission are different checks:

1. The plugin is registered and the target has a currently supported provider.
2. The caller's exact MCP group includes this tool surface.
3. The caller may access this organization, target and current device session.
4. The explicit operation grant is active and matches destination, scope and limits.
5. The provider connection and capability are still valid at dispatch time.

A wildcard tool group never supplies target, provider, call or SMS permission.
Revocation must block new operations immediately and terminate streams or
subscriptions as specified by their grant. Disconnect invalidates session
handles. Never reconnect an old handle to a different device.

Only registered root-provider capabilities contribute communication settings,
permission options and MCP tools. Preserve ordinary desktop configurations.
Settings use the plugin namespace, strict validation and safe defaults. A
configuration value cannot launch su or turn an ordinary service into root.

Registry filtering currently applies to the whole descriptor. Token-scoped MCP
exposure must not determine global administrative settings or event-handler
registration; adapters must keep these scopes separate. Construct registration
sinks before exposure and discard them if any contribution fails, because
registration does not roll back earlier sink writes.

## Proposed MCP surfaces

Names are provisional until registry compatibility tests are complete.

- Calls: start, answer, reject, hang up and query a caller-owned call handle.
- SMS: send and query delivery state using a unique idempotency key.
- Events: start/stop a bounded subscription to authorized call/SMS events.
- Audio: create an owned audio reference or attach to an authorized media session.
- Media: negotiate a transport handle over an existing authorized device route.

Every mutating operation needs a bounded idempotency key. Repeating an accepted
key returns the same operation identity; reusing it with a different payload
fails. An uncertain timeout must reconcile status before a retry. Do not infer
a successful call or SMS from an accepted command alone.

Inputs must be bounded and typed. Destination numbers use a validated canonical
form and explicit policy checks. Audio parameters refer to owned bounded
content, not arbitrary filesystem paths or HTTP URLs. RTP/SIP destinations come
from a negotiated route/session, not from unrestricted model input.

## Privacy and operational limits

Default communication grants and subscriptions are disabled. Read, send,
answer, observe, record and export are separate permissions. Listening for
events is not permission to record audio or retain SMS content.

Incoming SMS and caller-supplied text are untrusted content. Do not treat them
as authorization or instructions to invoke other tools. Do not automatically
extract or relay authentication codes.

Audit operation IDs, outcomes and redacted destination identifiers by default.
Do not log audio, complete phone numbers, message bodies, bearer tokens or SIP
credentials. Any retained content needs an explicit retention/deletion policy.

## Audio and transport compatibility

The reference audio implementation uses device-specific tinyalsa mixer/PCM
routes. Probe support per device and fail closed when unsupported; do not call
all Android root devices compatible. Preserve the existing desktop provider
when audio probing fails.

Prefer protected negotiated media. Plain SIP/RTP on public interfaces and
first-packet source learning are not acceptable defaults. Reuse LinkSphere's
authorized forwarding path where appropriate, but its length-framed UDP over a
reliable stream is not equivalent to an unconstrained real-time RTP network.
Measure latency, backpressure, reordering and teardown before advertising it.

## Acceptance gates

1. Registry and schema tests: uniqueness, deterministic exposure, immutability,
   unsupported runtime, old-agent compatibility and no implicit wildcard grants.
2. Mock provider tests: denied destinations, revoked access, disconnect,
   duplicate requests, conflicting retries, cancelled calls and SMS outcomes.
3. Mock transport tests: frame boundaries, bounded queues, malformed audio,
   sequence replay, stale source identity, deadline and cleanup.
4. Platform tests on an explicitly authorized Android root device: provider
   identity, ROM-specific call state, supported audio routing and permissions.
5. Separately authorized real communication trials with explicit destinations.
6. Update public marketing only for capabilities that passed the corresponding
   acceptance gate. Mock tests are not a successful live call or SMS.

## Local metadata factory

`serviceplugins.LocalCommunications` snapshots the fixed `call`, `audio`, and
`sms` capabilities of a trusted built-in provider. Unknown capabilities are
ignored. No metadata is available without a ready adapter, or outside an
Android process with effective UID 0. Returned metadata is defensively copied.
This factory is not registered in the central server and installs no command,
event, or MCP handler. Do not derive its provider from remote platform flags.
Readiness is a snapshot; future dispatch must recheck the live provider and
operation authorization. The registry tests include the Go race detector.

## Local dispatch admission

`NewLocalDispatcher` observes the current process platform and effective UID.
Only Android UID 0 can construct it; those observations and the provider identity
are rechecked on each admission, including after live authorization returns.
The immutable binding includes actor, target, provider, authenticated session,
and generation. The authorizer must authenticate the principal from its trusted
request context and check current grants, not trust a binding supplied by a client.
The provider identity is a local adapter observation; this does not solve remote
provider enrollment or establish trust in AgentHello platform/root fields.

Only `call`, `audio`, and `sms` capability operations are accepted. An event or
MCP adapter must map its operation to the exact originating capability and use
the same gate; there is no generic event, permission-grant, or arbitrary MCP
execution operation. The SDK in-memory transport is tested with fake handlers
and exact communication-group exposure, but that fixture is not a production
MCP registration path. Permission UI, streaming events, and Android device
adapters remain separate work.

Arguments must be a JSON object of at most 64 KiB and results valid JSON of at
most 1 MiB; boundary buffers are copied. Provider-side transient allocation is
outside these caps. The adapter validates operation-specific destinations and
payloads before any real side effect. No retries are performed automatically.
Closing the dispatcher rejects new admission and cancels active contexts; it
cannot undo a side effect already dispatched. The owning connection lifecycle
must close old handles on disconnect/reconnect; a new generation never revives
an old handle. No listener, OS permission change, or device action is installed
by constructing this component.
