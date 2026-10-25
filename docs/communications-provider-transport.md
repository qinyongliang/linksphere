# Opt-in local provider SSH transport

`serviceplugins.NewProviderTransport` is an unregistered transport foundation, not a call/audio/SMS dispatcher. Disabled returns nil without touching the supplied connection. Enabled takes ownership of a caller-supplied dedicated `net.Conn`, checks the actual runtime is Android UID0, and authenticates with the configured service signer against an exact copied server host-key pin using `x/crypto/ssh`. It neither dials nor listens, creates credentials, changes device settings, registers capabilities, nor exposes the SSH connection/channel API.

Both the host pin and signer public key reject certificate forms after parsing a public-key snapshot, including interface wrappers whose Marshal output contains a certificate. This follows the existing dedicated server authority's raw-key contract, not a new certificate/CA system. Signers are trusted local configuration and must provide a stable consistent key.

A positive timeout no greater than one minute and the caller context bound network handshake I/O. Setting/clearing deadlines and cancellation failures close the owned connection. Tracking starts as soon as a connection exists so every incoming channel and global request is rejected and drained even if post-handshake admission fails. Successful transport lifetime is explicit Close/peer disconnect; the finished handshake context is not retained as its lifetime. Public Done means invalidated, not proof all drain goroutines exited. Callers must bound concurrent constructors; a network deadline cannot forcibly terminate a non-cooperating custom Sign implementation or arbitrary custom net.Conn methods.

This transport proves a local process met the constructor's Android/root check and authenticated a configured SSH server/service key pair. It does not remotely attest UID0, authenticate a human actor, establish DispatchBinding, authorize an operation, or connect LocalDispatcher. Actor/session handoff and per-invocation authorization remain required. All incoming dispatch channels remain rejected; existing registry/dispatch/androidcall implementations are unchanged.

## Validation

Synthetic loopback SSH with an explicit test-only environment hook: final11 top-level tests/26 including subtests, repeated three times under race detector (78 passes); complete serviceplugins/protocol141 unique/423 passes; server Communications173 unique/519 passes; native three-package build passed. No races, skips or remaining test processes. Current source and prior input hashes matched.

Red/green evidence is retained: the same wrapped-certificate tests failed against the previous candidate because a certificate-bearing interface wrapper reached network admission; they pass after parsed-key rejection, asserting zero deadline/network admission calls. Tests also cover typed-nil inputs, wrong host pin/service key, blocked-handshake cancellation, natural timeout, deadline-clear cancellation, concurrent Close, all inbound request/channel rejection, and a real SSH rejected-reply write blocked until Close with drain completion. Deadline-failure tests assert owned raw connection closure only and do not overclaim drain evidence for that separate path.

No real Android/root-device, phone/audio/SMS, deployed application, or production acceptance is claimed. No GitHub Actions validation.
