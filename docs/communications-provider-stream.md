# Dedicated provider SSH stream adapter

This server-only slice supplies a private, explicitly opt-in stream opener for the existing bounded communications exchange. It is not wired into a listener, ordinary Agent, MCP endpoint, Android provider, or application startup. `streamOpener(false)` returns nil. It does not generate credentials or change device settings.

The root authority retains the exact authenticated SSH connection. Only its active authenticated connection can open the fixed outbound `linksphere-communications-v1` channel. Each dedicated transport allows one outstanding channel; excess work is rejected rather than queued. All peer-initiated channels and global requests remain rejected by the original authority.

The caller must provide a deadline no more than five minutes away. Cancellation or an earlier stream deadline invalidates and closes only that dedicated transport, including during a stalled OpenChannel or channel close. Deadline extension/clearing is rejected. Close waits for the channel request stream to drain before releasing its lease. Timeout callbacks compare the exact lease while locked and invalidate before releasing the lock; an obsolete callback cannot terminate a later operation. Existing exchange authorization and owner/org/generation revalidation are unchanged.

Tests use synthetic loopback SSH peers only. They cannot establish Android UID 0, device compatibility, remote actor authorization, actual calls/audio/SMS, or production readiness. The protocol envelope contains continuity fields, not trusted actor identity: do not reconstruct a local DispatchPrincipal from it or use an allow-all LocalDispatcher authorizer. Android/root eligibility remains a mandatory local-service condition. No product communications capability is enabled by these tests or this adapter.

## Validation

Final candidate: native build passed; five stream tests ran three times under the race detector (15 passes), complete Communications selection 173 unique tests/519 passes, and serviceplugins/protocol 115 unique tests/345 passes. No failures, skips, race reports or remaining test processes in the final run; frozen input hashes matched.

Earlier evidence is retained: one compile attempt stopped at the 550MiB disk guard before tests ran. An earlier stall fixture failed because the SSH library automatically acknowledges channel close even if application code does not call Close. The final fixture instead gates the actual underlying transport write until transport Close, preserves the deadline/invalidated/lease-release assertions, and verifies completion before the original context deadline. These synthetic results do not establish device communication or end-to-end provider authorization.
