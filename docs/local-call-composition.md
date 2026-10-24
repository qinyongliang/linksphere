# Default-off local call composition

NewLocalProvider composes a private Manager and call Provider only for explicitly enabled local initialization. Disabled configuration returns no provider and invokes no dependency. Enabled configuration requires an explicit destination policy, command runner and ready media resolver. Negative polling intervals are rejected before construction; zero retains the existing default. Platform, root and identity-shape checks are delegated to the existing local Provider.

This factory does not register anything, open a listener, install a service, grant permissions, or prove a remote device identity. No production call site is added. Callers must still use the trusted LocalDispatcher admission boundary. The server's production provider verifier and end-to-end Android wiring remain separate unfinished work. A self-reported root flag is not a substitute.

Mock tests cover default-off behavior, missing dependencies, invalid environment and identity, readiness loss, cancellation, destination denial and a synthetic successful lifecycle. Injected commands never execute OS processes in these tests. Tests do not establish real telephony, SMS or audio usability.
