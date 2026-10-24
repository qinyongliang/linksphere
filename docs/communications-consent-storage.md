# Communications consent storage

Migration 0009 adds explicit consent per user, target and operation (`call`,
`audio`, `sms`). Migration creates no grant rows. Existing target visibility,
administrator roles, desktop permissions, and wildcard MCP scopes do not create
communications consent.

The repository methods are persistence primitives, not a grant-management API
or a complete authorization decision. No device provider, network handler,
listener, token group or MCP tool is enabled by this change. Tests use disposable
SQLite databases and fake providers only.

## Stored state

Missing rows deny. Only exact operation names and boolean allow values are
accepted. Reads are fresh and uncached. Expiry is exclusive: a grant is invalid
at or after its expiry. Malformed timestamps fail closed rather than becoming
non-expiring grants. An explicit allowed row with no expiry represents persistent
consent; a future management UI must make that choice explicit. It is never a
migration or role default.

Revocation upserts a deny row and clears expiry, even if the supplied old expiry
has passed. Deleting a subject, target or granting user cascades the affected
consent rows. A disabled user is not the same as a deleted user: the eventual
execution authorizer must also reload user status, organization membership,
target visibility and provider/session binding on each operation.

This table stores current consent, not append-only audit history. Before exposing
any management endpoint, authenticate the actor and granting authority, add
transactionally consistent grant/revoke auditing, and retain per-call live
checks. A central database lookup does not by itself authenticate a remote Android
invocation. The server-to-agent protocol must bind trusted actor, target,
provider and current session generation, and the local provider must enforce
Android UID 0. Never substitute self-reported AgentHello values or user-supplied
identity fields for those checks.

MCP scope gates exposure only; events require the originating operation's live
consent. Revocation prevents subsequent admission but cannot undo an action
already sent to the device.
