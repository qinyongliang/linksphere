# Communications-only HTTP request lifetime

Communications tools and remote Android provider registration remain disabled.
This change does not add a provider, scope, listener, protocol operation or real
call/audio/SMS capability.

The MCP SDK deliberately separates HTTP cancellation from tool execution to
support stream resumption. Its server JSON-RPC connection retains values but
masks the incoming context's cancellation and deadline. A test expecting the raw
SDK tool context to cancel with HTTP failed; that expectation must not be used to
change ordinary MCP semantics or described as a real communications incident.

After authentication, the adapter now attaches a separate private lifetime from
Background, with the original deadline and an AfterFunc cancellation bridge.
Only the private communications exchange consumes it. Its call deadline is the
minimum of its own bound, the SDK context and this HTTP lifetime. Already-canceled
lifetimes are rejected before worker I/O. Normal MCP tools remain unchanged.

The worker receives a Background-derived context containing only cloned principal
identifiers and a digest. It receives the raw cancellation function, not the
caller-side release closure. Release stops parent callbacks on invoke return;
it is never passed to a late worker. Noncooperative workers continue to hold their
capacity slot until they actually finish. This does not remove the exchange's
existing bounded-timeout and noncooperative-close limitations.

## Verification

Actual StreamableHTTPHandler dispatch was exercised with httptest requests and
recorders, without sockets or device operations. Tests cover authenticated bearer
identity reaching a tool, cookie/loopback masking a foreign principal, 24 concurrent
requests with distinct bearer identities, and cancellation through the private
communications bridge. Separate tests cover earlier deadlines, already-canceled
parents, release, and exclusion of arbitrary context values from workers.

All communications-related server tests passed under the race detector with
count=3. Existing serviceplugins and protocol package tests also passed under
-race/count=3. These are selected server tests, not a complete server-package pass;
previous P2P environment and external SSH permission failures remain separate.

Trusted provider provisioning, possession proof, production transport wiring,
real communication capabilities, Android/root device acceptance and deployment
remain unverified or absent. Existing token scope normalization remains closed.
