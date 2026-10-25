# Fail-closed authorization dependency guard

The unregistered communications authorizer rejects a typed-nil repository before any binding callback or repository access. It reuses the existing nil-value helper rather than introducing a second reflection utility. A root-authored test failed before the guard and passed after it.

Validation: communications-selected tests across server, serviceplugins and protocol ran with -race -count=3: 165 top-level pass events, 525 including subtests, no failures. This is selected coverage, not the complete server suite.

A broader three-package -race -count=3 run was also attempted. Serviceplugins and protocol passed, but server failed: 13 reported test failures, 25 skipped events and the unchanged 10-minute test-process timeout. Those failures remain recorded and need separate diagnosis; this checkpoint does not claim the broad run passed or that all failures are caused by or independent of this guard.

No provider is registered, no listener or real communication is enabled, and no permission or token allowlist is broadened. A production trusted provider verifier and device end-to-end call/audio/SMS acceptance remain absent. This is a development checkpoint, not a production release.
