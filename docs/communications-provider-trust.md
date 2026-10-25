# Dedicated communications provider identity

The private provider authority is disabled by default. When explicitly constructed
by trusted startup code, it accepts only a dedicated connection authenticated with
an exact configured SSH public key. Ordinary Agent enrollment and self-reported
Android/root fields are not proof. The grant pins organization, target, Agent and
provider identifiers; public keys are copied and certificates are not accepted.

This establishes possession of an approved service key, not remote UID attestation.
The Android-side local dispatcher must independently require Android and UID 0.
No listener, configuration route, provider registration, command channel, media
exchange or MCP operation is enabled by this identity foundation. All SSH channels
and global requests are rejected. No key is generated or persisted by the authority.

Handshake and authenticated connection lifetimes share a bounded admission budget.
Closing the authority invalidates bindings immediately; capacity stays charged
until the owned connection finishes cleanup. Request cancellation owns the handshake,
not the lifetime of an already accepted transport. Trust grants are immutable:
close the old authority before constructing one with a changed trust set.

Session registration and each fresh current/pin lookup verify the current target
against the exact authenticated connection. Verification runs outside manager locks
and shares the bounded verifier budget. After verification the manager checks the
same entry and generation again. A late denial or success cannot affect or borrow
a replacement registration. Caller cancellation does not revoke a healthy shared
binding. A completed denial invalidates that binding without closing the transport.

These checks are point-in-time snapshots, not an atomic guarantee that a target
cannot change immediately afterward. Future operation adapters still need current
repository permission checks immediately before dispatch and request/lifetime
cancellation. Already dispatched external side effects cannot be undone by a later
permission change. Noncooperative host connection/verifier implementations cannot
be forcibly terminated by these abstractions.

Verification uses synthetic SSH peers and controlled session fixtures, including
unsigned probes, key mismatch, target/owner reassociation, late replacement results,
cancellation, reentrant close and verifier-budget retention. Native compilation and
communications/plugin/protocol race checks passed. This is not Android-device,
telephony, audio, SMS, production activation or full-server-suite acceptance.
