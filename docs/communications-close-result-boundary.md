# Local communications close/result boundary

LocalDispatcher.Close rejects new work and cancels the owned session. The
per-invocation cancellation callback is asynchronous, so its Context.Err alone
cannot establish that the dispatcher is still open when a provider returns.

Invoke now checks the owning closed/session state after validating and copying a
successful result, as well as the invocation context. A close that has already
completed therefore rejects the result even when cancellation forwarding has not
run yet. No provider call is retried, and no lock is held across provider work.
This check cannot undo a side effect already dispatched and is not a claim of
atomic external permission revocation.

The regression uses a fake provider and a test-only delayed cancellation callback.
It verifies the closed flag and canceled session while the invocation cancellation
is still gated, and waits for callback cleanup. It does not place a call, send SMS,
open a listener, register a production root provider or exercise a physical device.
