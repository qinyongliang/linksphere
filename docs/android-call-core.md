# Android call core and current integration boundary

## Prior evidence and source

The supplied Go call implementation was reported by the user to have passed real-call
tests. That prior evidence does not establish acceptance of the new LinkSphere plugin
path. Source archive SHA256:
30abe41519047fcb815b2f2e4a964c38493ceeb4ebfd1f6861c415d14e7f41f3.
The dependency-free call core was initially preserved byte-for-byte; subsequent
changes add the tested lifecycle and local adapter boundaries described below.

All four device-command sites use a per-manager trusted CommandRunner seam. A nil
runner retains the original OS command behavior; there is no global mutable or
request-supplied runner. Tests use fake commands and media, never device commands.

## Current cancellation and operation ownership

Cancel signals cancellation and returns the current snapshot, which may be nonterminal.
The worker owns hangup and terminal-history recording; repeated Cancel or cancellation
of an ended call does not issue another command. The slot remains busy until worker
cleanup finishes. This fixes the delayed Cancel hangup race, not every device hazard.
Legacy StartMedia retains its Background lifetime and legacy media-wait behavior; it
must not be directly connected to return-scoped dispatcher cancellation.

RunMediaContext is the separate synchronous, opt-in owned operation. It requires a
live context, rejects hangup_after_playback=false, and returns a final Call plus an
error for failed/cancelled outcomes only after the worker and started media callback
exit. It preserves number validation and voice/Telecom parsing. Cleanup has its own
five-second context and one worker-owned attempt. Cleanup failure or elapsed deadline
quarantines the manager with no automatic reset/retry. An uncooperative driver, media
or history callback can keep it busy indefinitely; cancellation is not proof of exit,
and a successful command alone does not prove the modem hung up.

Answer/media deadlines are recorded before child-context cleanup; late ACTIVE/nil
returns cannot turn an expired phase into success. Parent cancellation is checked
after cleanup and before terminal finalization, but this is a cooperative boundary,
not an atomic guarantee against cancellation racing the final snapshot/return.

## Local call provider, not registered transport

The Provider implements DispatchProvider for call/start only. Its public constructor
requires actual Android root, immutable target/provider/session/generation identity,
and a ready trusted media resolver. Host tests inject a private fake environment;
that is not remote device attestation. Requests are capped at 64 KiB and reject
unknown/duplicate keys, trailing JSON, wrong types, explicit null fields, unsupported
actions and hangup=false. Missing optional fields retain core defaults.

Audio IDs are opaque bounded ASCII IDs, not paths. The trusted resolver must enforce
context and target/session media scope and must not play audio during resolution.
A non-Android audio stub is not evidence of readiness. Context, environment, busy/
quarantine and media readiness are rechecked after resolution; the manager lock is
the final concurrent-start guard. Successful responses use a whitelist excluding
phone numbers, raw errors, paths and internal identity. Failures use a generic error.

Owned call observations now feed a bounded, authorized local event buffer through
an operation-owned publisher. Events contain only call ID, state and time. Command
readiness and supported event categories are separate, allowing authorized event
reads while a call is busy without permitting another call. There is no production
network event subscription, trusted remote enrollment or communications MCP transport.
See [local event buffer](call-event-buffer.md) for scope, cursor and cleanup limits.
Separate audio/SMS capabilities and generic status/list/cancel endpoints are not exposed.

## Remaining acceptance limits

Original Android ARM64 audio depends on missing patched third_party/purego and
third_party/anet directories. Do not silently substitute upstream versions or infer
readiness from stubs. The supplied archive contains no SMS implementation. The trusted
media-store implementation, remote session enrollment and activated end-to-end handlers
remain incomplete. Communications services remain unregistered and production scopes
remain denied.

Local mocked/race tests and Android ARM64 compilation do not prove real-device call,
audio, SMS, microphone or listener acceptance. No real communication, new credentials,
listener or device security change is part of this candidate or its tests.

### Close during trusted identity callbacks

Local admission rechecks dispatcher closure and its local session after trusted
identity callbacks return. Caller cancellation is also checked after the final
identity callback; an event reader discards a copied page if the dispatcher is
already closed at its final local check. This tightens a deterministic callback
window without holding a mutex across provider or authorizer code. It does not
claim atomic revocation of external identity/permission state, undo operations
already dispatched, or establish real-device/MCP transport acceptance.
