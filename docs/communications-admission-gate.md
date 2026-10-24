# Unregistered communications admission gate

The server-side gate is deliberately not wired to an HTTP endpoint, MCP tool,
Agent stream or device provider. It does not enable communications token scopes.
Existing persisted tokens cannot pass it; positive tests use an explicit fake
repository, while a disposable SQLite test verifies the actual repository still
denies communications scope.

Every check reloads the credential, actor status, target, membership/visibility
and exact operation consent. Wildcard scope, system-administrator status and
client-mode loopback convenience login do not supply explicit consent or scope.
Only a private authenticated principal digest is retained, not raw credentials,
request objects or user-supplied identity. Session-cookie authentication is not
implemented by this gate.

A future trusted session manager must supply the live actor, target, provider,
session, generation and authenticated Agent association. The gate checks that
snapshot before and after repository reads and checks expiry again at the final
boundary. Self-reported platform or root claims are not proof. Android root and
provider readiness remain independently enforced by the local dispatcher.

These are fresh admission checks, not a linearizable transaction with subsequent
device side effects. Revocation after a read cannot undo an already dispatched
action. Ongoing audio or event delivery still needs cancellation and repeated
checks for its originating operation. No permission-management endpoint may be
exposed without authenticated management authority and consistent auditing.

## Authenticated provenance adapter

The existing MCP bearer authentication branch now supplies a private principal
(user ID, token ID and SHA-256 digest) in the HTTP request context. Authentication
precedence is unchanged: a selected cookie or client-loopback identity supplies
no communications provenance, including mixed cookie/bearer requests. Those
paths shadow any earlier principal with an empty value. Cancellation and other
parent-context values are retained. No raw credential or request is retained.

This is provenance, not an authorization grant. The gate, communications tools,
provider sessions and transport remain unregistered; scope normalization still
excludes communications. Temporary SQLite tests cover actual credential selection
and normalization, plus actor-level provenance. They do not establish SDK
request-to-tool context propagation or real Android communication.

## Uninitialized dispatcher boundary

A nil or zero-value LocalDispatcher is not enrolled or initialized. Check, CheckEvents
and Invoke fail closed with ErrDispatchDenied; Invoke must reject missing session or
cancel state before registering context cancellation. Close is safe and idempotent
for nil/zero instances and permanently closes a zero instance without inventing a
session, default provider or permission. Normal constructed dispatchers retain their
existing session-cancellation behavior.

The regression uses no device command or real communication. It covers nil/zero
invocations, repeated close and concurrent zero-value close/invoke. It is a local
exported-type robustness fix, not evidence of remote exploitability or completed
Android call/SMS/audio integration. No capability is registered by this change.
