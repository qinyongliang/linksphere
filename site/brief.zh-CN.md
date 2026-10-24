# LinkSphere

## 让 AI 管理你的所有设备。

LinkSphere 是一个自托管的 AI 设备工作空间，将 Windows、macOS、Linux 和 Android 连接给你的 AI 助手。通过 MCP 使用设备终端、文件、桌面操作与服务连接，也可以直接打开浏览器工作台。

## 日常使用场景

- 让 AI 检查电脑或服务器，收集命令运行结果。
- 读取日志、编辑配置，在工作空间与设备之间传输文件。
- 观察并操作支持的 Windows、macOS 和 Android 桌面。
- 通过 TCP、UDP 和 HTTP 转发连接应用。
- 通过别名、文件夹和标签整理服务，与团队共享访问。

## 开始连接

1. 部署 LinkSphere，打开控制台。
2. 添加服务，使用默认的“节点”选项安装 Agent。
3. 在“AI 接入与凭证”中创建 MCP Token、选择工具组，接入 AI 助手。

也可以使用标准 SSH。请替换示例域名、端口和服务别名：

```sh
ssh -p 22022 demo-device@linksphere.example.com "uptime"
```

## 平台能力说明

Windows、macOS 和 Android 在完成本机设置与授权后支持桌面操作。Linux 当前支持终端、文件和服务管理，暂不支持原生桌面控制。Android 屏幕操作需要配对的无障碍辅助组件，或具备 root/ADB 访问的已解锁设备。

文件传输优先尝试加密 P2P，无法直连时中转，不保证所有网络均可直连。网页上传限制为 1 GiB。LinkSphere 提供设备工具，由接入的 AI 助手规划并执行任务。

面向 Android root 设备的通信插件架构，为通话、音频、短信与事件提供统一的扩展入口，并按设备能力和明确授权控制访问。通信能力需由受信任的 root 服务提供；当前版本尚未开放远程通信与对应 MCP 工具，具体设备支持以完成接入后的验证结果为准。

## 可直接使用的发布文案

认识 LinkSphere：让 AI 管理你的所有设备。将 Windows、macOS、Linux 和 Android 接入一个自托管工作空间，让 AI 助手运行命令、管理文件、操作支持的桌面并连接服务。从第一台设备开始，把 AI 带进日常工作。

[GitHub](https://github.com/qinyongliang/linksphere) · [Download](https://github.com/qinyongliang/linksphere/releases/latest)
