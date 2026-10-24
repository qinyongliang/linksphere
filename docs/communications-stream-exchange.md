# Private bounded communications exchange

The server has an unregistered request/reply exchange adapter that composes
trusted provider-session pinning, fresh authorization and the bounded codec.
It is not called by AgentHello, ordinary Agent streams, HTTP routes or MCP tools.
It does not add a trusted verifier, device-verifiable destination authority,
replay protection, event subscriptions or actual device capabilities.

Each adapter has a nonblocking capacity gate (1–64 exchanges) and a finite timeout
(at most five minutes). One absolute deadline covers authorization, opening and
I/O, and cannot extend the caller's earlier deadline. Arguments are copied before
asynchronous use. Identifiers are validated and cloned; a worker context retains
only the private authenticated principal, not arbitrary request context values.
Caller and pinned-binding cancellation are linked explicitly.

A worker owns its capacity slot through real stream cleanup. If an opener,
authorizer, deadline setter or stream Close ignores cancellation, the caller can
still time out, but that slot remains occupied until the work actually finishes.
This bounds abandoned work; it does not promise termination of uncooperative host
code. Capacity is per adapter, not a process-wide heap or goroutine budget.

Authorization and session freshness are checked before opening, immediately
before writing, and before returning a reply. The exact verified connection is
passed to the injected opener; ordinary registry fallback is forbidden. Version,
kind and all request correlation fields are validated. UUIDs are generated inside
the adapter. Callback and transport errors are not exposed verbatim.

Only the owned stream is closed, exactly once. The parent connection is never
closed. There is no retry after a partial transmission. Timeout or stream closure
cannot prove that an already transmitted device operation did not execute or was
stopped. Consent checks are boundary snapshots, not atomic external revocation.

Tests use isolated in-memory streams and fake trusted verifier/authorization
callbacks. They cover capacity held across late opening/blocked cleanup, caller
cancellation, session revoke/replacement, invalid replies, argument ownership,
deadlines and failure without retry. These are not real call, SMS, audio or
Android-device acceptance, and do not authorize exposing a communications route.
