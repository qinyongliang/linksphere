# Android communications provider-session seam (unpublished)

This first stage is an unexposed server-side lifecycle manager. It creates no
HTTP/MCP endpoint, AgentHello field, listener, token scope, credential or actual
communications provider. Production token scopes still exclude communications.
It is not Android attestation, enrollment approval or a remote transport pass.

## Trust boundary

Ordinary Agent enrollment proves an Agent association, not that a connection is
an approved Android UID-0 provider. The manager requires an explicit trusted host
verifier; a missing or typed-nil verifier rejects all registrations. Candidates
contain only Agent/target/provider identifiers and the exact connection object.
No caller-supplied actor, session ID, generation, OS or root flag is accepted.
The verifier must independently establish the approved provider association; a
permissive test verifier is not a production implementation of that requirement.

The host verifier is side-effect-free and context-aware. Each connection supplies
a stable non-nil close channel. The manager assigns a random session identifier
and monotonic nonzero generation; those are continuity identifiers, not grants or
credentials. Changing an existing Agent association requires revoke before fresh
registration. Entropy failure, exhausted generation, invalid identifiers and
incomplete initialization fail closed without replacing an existing live entry.

## Lifetime and bounds

Live/pending scopes and actual active verification calls are bounded per manager
by its constructor limit. There is one pending attempt per target/provider scope.
Revoke/Close and connection closure cancel verification, but an unresponsive
verifier remains charged until it actually returns and its verification watcher
has drained. No forced termination or process-wide memory limit is implied.

Entries pin the original connection. Replacement cancels the old owned binding
lifetime before publishing the new entry. Late verification or old-connection
cleanup cannot erase its replacement. Revoke/Close cancel owned binding contexts,
not ordinary Agent transports; unrelated desktop features are not disconnected.
Session watcher cleanup remains asynchronous and does not imply immediate GC.
The manager does not hold its lock across verifier, entropy or caller
context methods/cancellation callbacks. Owned binding cancellation uses only standard
Background-derived contexts.

Current snapshots derive actor identity only from the private bearer principal.
A snapshot/pin is not authorization: repository token, user, target and exact
operation grants must still be checked by communicationsAuthorizer on every call.
Pins return the exact connection and owned lifetime, never an AgentID lookup that
could silently resolve a newly connected session. Future transports must combine
request/lifetime cancellation and reauthorize before dispatch. This does not undo
already-started external side effects or promise atomic remote revocation.

## Remaining work and evidence limits

A real trusted provider verifier, versioned bounded protocol, device-verifiable
call authority/replay controls, remote stream adapter and audited management/MCP
registration are not implemented by this file. The Android-local dispatcher must
still independently enforce Android UID 0. Do not widen token allowlists or accept
self-reported root metadata to make the seam appear connected.

Tests use fake close channels and explicit fake verifiers. They exercise denial,
identity pinning, replacement, cancellation, capacity, late callbacks and cleanup
without network listeners, devices, calls, SMS, microphone capture or credential
creation. Existing authorization/local-plugin selected tests remain separate from
real Android/root, full-server, remote-transport or production acceptance.
