# LinkSphere

**让 AI 管理你的设备。**

[English](README.md) · [官网](https://qinyongliang.github.io/linksphere/index.zh-CN.html) · [发布版本](https://github.com/qinyongliang/linksphere/releases) · [产品介绍](site/brief.zh-CN.md)

![LinkSphere 连接电脑、服务器与手机](site/assets/connected-devices-1536.webp)

LinkSphere 是一个自托管的设备工作空间，把 Windows、macOS、Linux 和 Android 连接给你的 AI 助手。它提供终端、文件、支持的桌面操作和内网服务连接工具，既可以在浏览器中使用，也可以接入支持 MCP 的 AI 工具。

## 可以做什么

| 场景 | 能力 |
| --- | --- |
| 接入设备 | Agent 节点、已有 SSH 连接、别名、文件夹与标签 |
| 接入 AI | MCP 接口、可选择的工具组与访问范围 |
| 执行命令 | 网页终端、标准 SSH 客户端、批量命令 |
| 管理文件 | 浏览、编辑、上传文件夹、下载、复制与移动 |
| 操作桌面 | 观察窗口并执行受支持的原生操作 |
| 连接服务 | 持久 TCP、UDP 和 HTTP 转发 |
| 团队协作 | 组织、成员、访问策略与操作记录 |

LinkSphere 提供设备工具，由接入的 AI 助手规划和执行任务。官网图片和动画用于说明使用场景，并非实时设备画面。

## 平台支持

| 设备 | 终端与文件 | 原生桌面操作 |
| --- | --- | --- |
| Windows | 支持 | 需要已登录桌面和本机权限 |
| macOS | 支持 | 需要已登录桌面、屏幕录制和辅助功能权限 |
| Linux | 支持 | 暂不支持 |
| Android | Agent APK 或受支持的 Shell 后端 | 配对的无障碍辅助组件，或已解锁设备上的 root/ADB |

可用能力取决于设备、已安装组件和系统权限。详细要求见[桌面控制](docs/desktop-mcp.md)与 [Android 接入](docs/android-accessibility.md)。

## 下载安装包

首个版本为 **v0.1.0**。安装前请核对发布页中的 SHA256 校验文件。

| 使用方式 | 下载 |
| --- | --- |
| Windows 桌面客户端 | [安装器](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/LinkSphere-Setup-v0.1.0-windows-amd64.exe) · [免安装 ZIP](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/LinkSphere-v0.1.0-windows-amd64-portable.zip) |
| Linux 服务端 | [x64](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/gosshd-v0.1.0-linux-amd64.tar.gz) · [ARM64](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/gosshd-v0.1.0-linux-arm64.tar.gz) |
| macOS 服务端 | [Apple Silicon](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/gosshd-v0.1.0-darwin-arm64.tar.gz) · [Intel](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/gosshd-v0.1.0-darwin-amd64.tar.gz) |
| Windows 服务端 | [x64 ZIP](https://github.com/qinyongliang/linksphere/releases/download/v0.1.0/gosshd-v0.1.0-windows-amd64.zip) |

Windows 安装器尚未进行 Authenticode 签名，macOS 包未公证。桌面 MCP、Agent 及其校验文件见[完整发布页](https://github.com/qinyongliang/linksphere/releases/tag/v0.1.0)。Android 正式 APK 需要单独配置维护中的签名密钥，不会用开发签名替代正式签名。

## 本地运行

准备 Go 1.26+、Node.js 24，以及 `package.json` 指定版本的 pnpm。

```sh
git clone https://github.com/qinyongliang/linksphere.git
cd linksphere
corepack enable
pnpm install --frozen-lockfile
pnpm run build
go build -o bin/gosshd-server ./cmd/gosshd-server
```

设置自己的管理员密码，并启动仅供本机访问的实例：

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

打开 `http://127.0.0.1:18080`，使用 `admin` 和刚设置的密码登录。Windows 下使用 `go build -o bin/gosshd-server.exe ./cmd/gosshd-server` 构建，再把相同启动参数写在一行 PowerShell 命令中执行。

上述配置仅允许本机连接。接入远程设备前，需要配置可达地址、HTTPS 和必要的防火墙规则，不要直接将本地示例暴露到公网。

已构建的软件包会放在[发布版本](https://github.com/qinyongliang/linksphere/releases)中。为兼容现有安装，可执行文件名、环境变量和安装包标识保留既有命名；产品名称统一为 LinkSphere。

## 接入设备与 AI

1. 打开**服务 → 添加服务**，默认选择**节点**。
2. 选择设备平台，在对应设备上执行生成的安装命令。已有 SSH 连接可改选**直连服务**。
3. 打开 **AI 接入与凭证**，创建 MCP Token，选择助手需要的工具组。
4. 将生成的配置复制到支持 MCP 的 AI 工具中。

下面只展示配置结构，请替换示例地址与 Token：

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

也可以使用标准 SSH。公钥用于识别用户，SSH 用户名对应服务别名：

```sh
ssh -p 22022 demo-server@linksphere.example.com "uptime"
```

## 数据与访问

- 将数据库、主机密钥和加密密钥放在持久存储中，保护加密密钥，并为密钥与加密数据制定配套恢复方案。
- MCP Token 和临时授权都是访问凭证。只选择需要的工具组，用完及时撤销不再需要的授权。
- 原有访问策略与命令审核能力继续可用。命令审核不是完整 Shell 沙箱；桌面控制还需要操作系统原生权限。
- 文件传输优先尝试加密 P2P，无法直连时中转，实际链路取决于网络条件。
- 网页上传上限为 **1 GiB**。浏览器无法直接流式写入磁盘时，大文件使用原生 HTTP 下载，避免在内存中缓冲超过 **256 MiB**。

## 使用文档

- [Agent 安装](docs/agent-installation.md)
- [Session MCP 与通用文件工具](docs/session-mcp.md)
- [桌面 MCP](docs/desktop-mcp.md)
- [Android 无障碍接入](docs/android-accessibility.md)
- [Android 桌面后端](docs/android-desktop-mcp.md)
- [TCP 与 UDP 转发](docs/tunnels.md)
- [HTTP 转发](docs/http-forwarding.md)
- [HTTP 转发架构](docs/http-forward-architecture.md)
- [桌面客户端](client/wails/README.md)

## 开发与验证

```sh
pnpm run check
pnpm run build
go test ./...
node --test scripts/marketing-site.test.mjs
node scripts/check-public-content.mjs
```

运行浏览器回归前，将 `GOSSHD_UI_E2E_PLAYWRIGHT` 指向已安装的 Playwright 包，将 `GOSSHD_UI_E2E_BROWSER` 指向 Chromium 可执行文件，然后运行：

```sh
node --test scripts/marketing-site.test.mjs scripts/linksphere-ui.test.mjs
```

界面回归使用虚构账号和保留示例地址。生成的截图、日志、数据库和本地操作记录应放在 Git 忽略目录中。

发布前暂存计划公开的文件，执行 `node scripts/check-public-content.mjs --staged`，并审阅 `git diff --cached`。第三方许可证和署名应继续保留。

Android 正式发布需要另行配置签名 Secrets。普通分支构建使用明确标识的开发包，不发布正式 APK。详见[发布签名](docs/android-accessibility.md#maintained-release-signing-on-github)。
