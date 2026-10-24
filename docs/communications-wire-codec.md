# Unregistered communications codec

This module supplies only a bounded version-1 request/reply codec. It is not
registered with ordinary Agent streams, HTTP routes, MCP tools or device services.
Existing enrollment metadata does not prove Android root identity. No token scope
allowlist is expanded, and no credentials, calls, messages or audio are enabled.

Each frame has a four-byte big-endian length followed by a strict JSON object.
The expected direction is supplied by the caller. Requests carry at most 64 KiB
of JSON-object arguments; successful replies carry at most 1 MiB of valid JSON.
Each direction allows an additional 8 KiB for envelope fields. The reader rejects
oversized declared lengths before allocating a body. Unknown or duplicate envelope
members (including escaped duplicate names), missing fields, invalid directions,
versions, operations, identifier spellings and opposite-direction fields fail.
Arguments and results retain the existing local dispatcher's JSON semantics.
Envelope identifier lone-surrogate escapes are rejected instead of normalized.
HTML characters in raw payloads are not expanded by serialization.

Version, request, target, provider, session, generation and operation fields are
continuity data, not authorization. A future transport must pin the exact verified
connection, reauthorize, compare all reply correlation fields, and reject stale
bindings before returning a result. This codec does not supply that adapter,
trusted root verification, device-verifiable authority or replay protection.

The transport must own finite deadlines, bounded concurrent opens/exchanges and
stream cancellation. Writes handle positive short counts. Any error can follow a
partial transmission; no automatic retry is safe. Closing a stream cannot prove
that a previously dispatched device operation stopped. Error replies and event
subscriptions are intentionally not introduced by this success-reply codec.
