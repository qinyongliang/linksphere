# LinkSphere

## Let AI manage all your devices.

LinkSphere is a self-hosted workspace that connects Windows, macOS, Linux and Android to your AI assistant. Use MCP to work with device terminals, files, desktop actions and service connections, or open the browser workspace yourself.

## Everyday workflows

- Ask AI to inspect a computer or server and collect command results.
- Read logs, edit configuration and transfer files between your workspace and devices.
- Observe and operate supported Windows, macOS and Android desktops.
- Connect applications through TCP, UDP and HTTP forwarding.
- Organize services with aliases, folders and tags, and share access with your team.

## Get connected

1. Deploy LinkSphere and open the console.
2. Add a service using the default Node option and install the Agent.
3. Create an MCP token in AI & credentials, choose tool groups, and connect your AI assistant.

Standard SSH remains available. Replace the example host, port and service alias:

```sh
ssh -p 22022 demo-device@linksphere.example.com "uptime"
```

## Platform capabilities

Windows, macOS and Android support desktop actions after local setup and permissions. Linux currently supports terminal, file and service workflows, without native desktop control. Android screen actions require a paired accessibility companion or an unlocked device with root/ADB access.

File transfers try encrypted P2P first and use relay when unavailable; direct connections are not guaranteed. Web uploads have a 1 GiB limit. LinkSphere provides tools; the connected AI assistant plans and executes tasks.

A plugin architecture for Android root devices provides a shared foundation for calls, audio, SMS and events, with access governed by device capabilities and explicit permissions. Communication features require a trusted root service. Remote communication and its MCP tools are not yet available in this version; device support requires validation after integration.

## Ready-to-share announcement

Meet LinkSphere: let AI manage all your devices. Connect Windows, macOS, Linux and Android in one self-hosted workspace. Give your AI assistant the tools to run commands, manage files, operate supported desktops and connect services. Start with your first device and bring AI into your everyday workflow.

[GitHub](https://github.com/qinyongliang/linksphere) · [Download](https://github.com/qinyongliang/linksphere/releases/latest)
