# LinkSphere

**Let AI manage your devices.**

[简体中文](README.zh-CN.md) · [Website](https://qinyongliang.github.io/linksphere/) · [Releases](https://github.com/qinyongliang/linksphere/releases) · [Product brief](site/brief.en.md)

![LinkSphere connects computers, servers and phones](site/assets/connected-devices-1536.webp)

LinkSphere is a self-hosted workspace that connects Windows, macOS, Linux and Android to your AI assistant. It provides tools for terminals, files, supported desktops and private services. Work in the browser or connect an MCP-compatible assistant.

## What it does

| Workflow | Capabilities |
| --- | --- |
| Connect devices | Agent nodes, existing SSH connections, aliases, folders and tags |
| Work with AI | MCP endpoint with selectable tool groups and scoped access |
| Run commands | Browser terminals, existing SSH clients and batch commands |
| Manage files | Browse, edit, upload folders, download, copy and move files |
| Operate desktops | Observe windows and perform supported native actions |
| Connect services | Persistent TCP, UDP and HTTP forwarding |
| Work with a team | Organizations, members, access policies and operation records |

LinkSphere provides tools; the connected assistant plans and executes tasks. The website's images and animations are illustrations, not live device sessions.

## Platforms

| Device | Terminal and files | Native desktop actions |
| --- | --- | --- |
| Windows | Supported | Requires a logged-in desktop and local permissions |
| macOS | Supported | Requires a logged-in desktop, Screen Recording and Accessibility permissions |
| Linux | Supported | Not currently supported |
| Android | Agent APK or a supported shell backend | Paired accessibility companion, or root/ADB on an unlocked device |

Capabilities depend on the device, installed components and OS permissions. See [desktop control](docs/desktop-mcp.md) and [Android setup](docs/android-accessibility.md).

## Download packages

The first release is **v0.1.0**. Verify the published SHA256 checksums before installing.

| Use | Download |
| --- | --- |
| Windows desktop client | [Installer](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/LinkSphere-Setup-v0.1.0-windows-amd64.exe) · [Portable ZIP](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/LinkSphere-v0.1.0-windows-amd64-portable.zip) |
| Linux server | [x64](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/gosshd-v0.1.0-linux-amd64.tar.gz) · [ARM64](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/gosshd-v0.1.0-linux-arm64.tar.gz) |
| macOS server | [Apple Silicon](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/gosshd-v0.1.0-darwin-arm64.tar.gz) · [Intel](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/gosshd-v0.1.0-darwin-amd64.tar.gz) |
| Windows server | [x64 ZIP](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/gosshd-v0.1.0-windows-amd64.zip) |

Windows installers are not Authenticode-signed and macOS packages are not notarized. Desktop MCP packages, Agents and checksums are on the [full release page](https://github.com/qinyongliang/linksphere/releases/tag/v0.1.0). Formal Android APKs require the maintained signing configuration; development signatures are not substituted.

## Run locally

Requirements: Go 1.26+, Node.js 24 and the pnpm version declared in `package.json`.

```sh
git clone https://github.com/qinyongliang/linksphere.git
cd linksphere
corepack enable
pnpm install --frozen-lockfile
pnpm run build
go build -o bin/gosshd-server ./cmd/gosshd-server
```

Start a local-only console with a password of your choice:

```sh
./bin/gosshd-server \
  --http-listen 127.0.0.1:18080 \
  --ssh-listen 127.0.0.1:22022 \
  --public-host 127.0.0.1:18080 \
  --database-path ./data/linksphere.db \
  --secret-key-path ./data/linksphere.secret-key \
  --audit-database-path ./data/linksphere-activity.db \
  --host-key-path ./data/linksphere-host-key \
  --agent-cache-path ./agent-cache \
  --bootstrap-admin-password '<choose-a-strong-password>'
```

Open `http://127.0.0.1:18080` and sign in as `admin` with the password you selected. On Windows, build the executable with `go build -o bin/gosshd-server.exe ./cmd/gosshd-server`; pass the same options on one line in PowerShell.

This configuration only accepts local connections. Before connecting remote devices, configure reachable addresses, HTTPS and the required firewall rules. Never expose the example configuration unchanged.

Release packages, when available, are published under [Releases](https://github.com/qinyongliang/linksphere/releases). Existing executable names, environment variables and installed package identifiers remain compatible; the product name is LinkSphere.

## Connect a device and an AI assistant

1. Open **Services → Add service**. The default option is **Node**.
2. Choose the device platform and run the generated installer on that device. For an existing SSH endpoint, choose **Direct connection** instead.
3. Open **AI & credentials**, create an MCP token and select the tool groups the assistant needs.
4. Copy the generated configuration into your MCP-compatible AI tool.

Example configuration; replace the reserved example host and token:

```json
{
  "mcpServers": {
    "linksphere": {
      "url": "https://linksphere.example.com/mcp",
      "headers": { "Authorization": "Bearer <MCP_TOKEN>" }
    }
  }
}
```

Standard SSH access remains available. Your SSH public key identifies you, and the SSH username selects the service alias:

```sh
ssh -p 22022 demo-server@linksphere.example.com "uptime"
```

## Data and access

- Store databases, host keys and the encryption key in persistent storage. Protect the encryption key and plan how to recover it alongside encrypted data.
- Tokens and temporary access grants are credentials. Select the minimum tool groups and revoke grants when they are no longer needed.
- Access policies and command review remain available. Command review is not a complete shell sandbox; desktop control separately requires native OS permissions.
- File transfers try encrypted P2P and fall back to relay when a direct path is unavailable. Direct connectivity depends on the network.
- Browser uploads are limited to **1 GiB**. Where a browser cannot stream downloads to disk, larger downloads use the native HTTP path rather than buffering more than **256 MiB**.

## Documentation

- [Agent installation](docs/agent-installation.md)
- [Session MCP and portable file tools](docs/session-mcp.md)
- [Desktop MCP](docs/desktop-mcp.md)
- [Android accessibility setup](docs/android-accessibility.md)
- [Android desktop backend](docs/android-desktop-mcp.md)
- [TCP and UDP forwarding](docs/tunnels.md)
- [HTTP forwarding](docs/http-forwarding.md)
- [HTTP forwarding architecture](docs/http-forward-architecture.md)
- [Desktop client](client/wails/README.md)

## Development and verification

```sh
pnpm run check
pnpm run build
go test ./...
node --test scripts/marketing-site.test.mjs
node scripts/check-public-content.mjs
```

For browser regression tests, set `GOSSHD_UI_E2E_PLAYWRIGHT` to an installed Playwright package and `GOSSHD_UI_E2E_BROWSER` to a Chromium executable, then run:

```sh
node --test scripts/marketing-site.test.mjs scripts/linksphere-ui.test.mjs
```

The UI regression tests use synthetic accounts and reserved example addresses. Generated screenshots, test logs, databases and local operational records belong in ignored directories.

Before publishing, stage the intended files, run `node scripts/check-public-content.mjs --staged`, and review `git diff --cached`. Preserve third-party license and attribution notices.

Signed Android releases require separately configured signing secrets. Ordinary branch builds use explicitly named development packages; they do not publish release APKs. See [release signing](docs/android-accessibility.md#maintained-release-signing-on-github).
