# Verification callback failure cleanup

A trusted provider verifier may panic. The session manager continues to propagate
that panic unchanged, allowing the enclosing host to apply its normal recovery
policy; it never converts a failed verifier into successful authorization.

Revalidation now installs deferred admission release immediately after reservation,
before constructing the child context. Child cancellation and lifetime-hook cleanup
are also deferred. This prevents a recovered verifier failure from permanently
occupying capacity or retaining its request context. Cleanup still waits for the
actual verifier call to finish or unwind; ordinary cancellation does not release
capacity while a noncooperative verifier is still executing.

The regression reproduces the old uncanceled context, then checks the original panic,
canceled verification context, zero active slots and reuse of the healthy binding.
Existing replacement, cancellation and blocked-verifier race checks remain intact.
This does not enable a listener, register a device, or perform real communication.
