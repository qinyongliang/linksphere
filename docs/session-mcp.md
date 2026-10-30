# Session MCP

The `session` tool group can open persistent terminals without opening the web
console. The server keeps shell state in memory, including the working directory
and environment variables. Sessions survive individual MCP requests and client
reconnections; they end when closed, idle for ten minutes, or the server stops.
Active operations do not count as idle. Session ids belong to the authenticated
user and cannot be used by another user.

## Workflow

1. Call `session_targets` to discover accessible targets with a session-only token.
2. Call `session_create` with `{"target_id":"example-target-id"}`. Creation waits
   for shell integration to become ready and returns the new session `id`,
   `managed: true`, and `current_directory` when reported by the shell.
3. Pass that id as `session_id` to `session_send_command`, `read`, `edit`, or
   `write`. Commands execute in the same shell, so `cd` and exported variables
   remain available to later commands.
4. Use `session_list` to find your web and MCP sessions. Inspect a busy shell with
   `session_screen` or stop its command with `session_interrupt`.
5. Call `session_close` with `{"session_id":"example-session-id"}` when finished.

MCP-created sessions need no browser heartbeat. Existing web-terminal
collaboration remains available. Target visibility, terminal policies, command
review and audit still apply. A shell backend must support LinkSphere's terminal
integration; failed or timed-out startup does not return a usable session.

Multiline Unix commands require the bundled Bash integration and keep the same
working directory and exported variables as other session commands. If that
integration is unavailable, update the Agent or open a new Bash session before
sending a multiline command.

Agent Bash sessions negotiate a dedicated interruption capability. An explicit
MCP interrupt signals the session's own shell before sending terminal Ctrl+C,
so an interrupted multiline script does not continue with its remaining lines.
Older Agents must be updated before they accept multiline commands. Ordinary
keyboard input, Windows command framing, and direct SSH Ctrl+C keep their
existing behavior.
Custom shell signal handlers keep their normal semantics.

Closing an Agent shell session also terminates that session's own shell and
closes its terminal; it does not stop the Agent process.

These multiline commands run as a sourced Bash script with terminal stdin still
available. `BASH_SOURCE` and `LINENO` describe that script, a top-level `return`
ends that command, and process substitution can update `$!`. The persistent
working-directory and environment contract remains the same.

## Portable text-file tools

These tools use SFTP through the session's SSH or Agent target on supported
Windows, macOS, Linux and Android backends. They do not depend on shell quoting,
`sed`, PowerShell, or other platform-specific command syntax.

| Tool | Arguments in addition to `session_id` | Behavior |
| --- | --- | --- |
| `read` | `path`, optional `offset`, optional `limit` | Read UTF-8 text; line offsets start at 1. |
| `edit` | `path`, `edits: [{oldText, newText}]` | Apply exact replacements to the original file. |
| `write` | `path`, `content` | Create or overwrite a UTF-8 text file, creating parent directories. |

The parameter names follow [Pi's file tools](https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent/src/core/tools).
For example:

```json
{"session_id":"example-session-id","path":"config/app.json","offset":1,"limit":80}
```

```json
{"session_id":"example-session-id","path":"config/app.json","edits":[{"oldText":"\"enabled\": false","newText":"\"enabled\": true"}]}
```

Use `edit` for targeted changes and `write` for a new file or complete rewrite.
Every `oldText` must occur exactly once; missing, repeated, and overlapping
matches fail before any file write. Edits normalize matching to LF and preserve
CRLF output for files using Windows line endings.

Paths can be absolute or relative to the shell's reported working directory.
Both Windows drive paths such as `C:/example/config.json` and POSIX paths such as
`/srv/example/config.json` work regardless of the server's OS. If no working
directory is available, use an absolute path. Windows drive-relative paths such
as `C:config.json` are rejected as ambiguous.

Text files and written content are limited to 2 MiB. `read` defaults to at most
2,000 lines and 50 KiB per response. When `truncated` is true, continue using
`next_offset`. Binary files, invalid UTF-8, and individual lines over the output
limit are rejected. Use the existing file-transfer features for binary or larger
files.

File access is checked on every call. `read` requires download permission,
`write` requires upload permission, and `edit` requires both. Each operation has
a session-linked SFTP audit entry. File tools do not execute shell commands.

File tools return a busy error while another command or file operation owns the
session. Retry after it finishes, or inspect and interrupt the active command.
If a target's connection settings change, create a new session before using its
file tools. Writes stage content next to the destination and replace the original
only after successful completion. Writing to a symbolic link is rejected; use the
resolved target file path instead.
