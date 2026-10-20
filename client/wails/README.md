# LinkSphere desktop client

A Wails desktop shell for the local single-user LinkSphere workspace.

Build from the repository root. The web build also prepares the bindings embedded by the native client. The wrapper uses the shared Go module.

```sh
pnpm run build
go build -o bin/gosshd-client.exe ./client/wails
```

The command above targets Windows. For a packaged Windows application, run `./build-client-windows.ps1` in PowerShell; it installs the pinned Wails CLI if needed and builds the package. The existing executable identifier and `GOSSHD/Client` data directory are retained for installed-client compatibility.

The client exposes local terminal, file, forwarding and MCP workflows. Native desktop control follows the same OS permission requirements described in [desktop MCP](../../docs/desktop-mcp.md).

Keep local application data, pairing tokens, signing keys and build artifacts out of Git.
