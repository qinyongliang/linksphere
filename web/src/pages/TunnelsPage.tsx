import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Card,
  Checkbox,
  ConfigProvider,
  Input,
  InputNumber,
  Select,
  Switch,
  Tag,
  Tabs,
  Tooltip,
  theme as antdTheme,
} from "antd";
import {
  ArrowRight,
  ChartNoAxesCombined,
  Clock3,
  Globe,
  Network,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Search,
  Server,
  Square,
  Trash2,
} from "lucide-react";
import { useState } from "react";
import { api } from "../api";
import { HTTPForwardEditor, HTTPForwardSummary, newHTTPConfig } from "../components/HTTPForwardEditor";
import { EgressEditor, egressCopy, egressProblem, egressSummary } from "../components/EgressEditor";
import { ForwardLogsDialog } from "../components/ForwardLogs";
import { MachinePicker } from "../components/MachinePicker";
import { TunnelErrorNotice } from "../components/TunnelErrorNotice";
import {
  ConfirmDialog,
  ErrorMessage,
  Modal,
  Segmented,
} from "../components/ui";
import { useI18n } from "../i18n";
import { useTheme } from "../theme";
import { TunnelPathStatus } from "../components/TunnelPathStatus";
import {
  TunnelTrafficDialog,
  formatBytes,
  tunnelTrafficCopy,
} from "../components/TunnelTraffic";
import type { ConsoleData, PortRule, Target, Tunnel, TunnelConfig } from "../types";

const copy = {
  "zh-CN": {
    title: "隧道管理",
    unknown: "离线 · 状态待确认",
    creator: "创建者",
    temporary: "临时隧道",
    managed: "持久配置",
    source: "来源地址",
    localUnknown: "客户端 · 监听地址未知",
    remoteUnknown: "目标地址未知",
    sshClient: "客户端",
    temporaryHelp: "客户端断开后结束，可在此停止。",
    create: "新建隧道",
    edit: "编辑隧道",
    search: "搜索隧道或目标地址",
    all: "全部状态",
    running: "运行中",
    starting: "连接中",
    error: "连接失败 · 自动重试",
    stopped: "已停止",
    expired: "已到期",
    entry: "入口",
    exit: "出口",
    destination: "目标服务",
    bastion: "服务端",
    relay: "中转",
    access: "访问者",
    direct: "直接连接",
    listen: "监听地址",
    listenHint: "在入口机器监听。127.0.0.1 仅本机可访问。",
    destinationHint: "从出口机器访问；127.0.0.1 表示出口机器自身。",
    port: "监听端口",
    portForward: "端口转发",
    protocols: "传输协议",
    selectProtocol: "请至少选择 TCP 或 UDP。",
    udpSSH: "UDP 仅支持服务端和 Agent 端点；SSH 机器只支持 TCP。",
    targetHost: "目标地址",
    targetPort: "目标端口",
    entryMachine: "入口机器",
    exitMachine: "出口机器",
    name: "隧道名称",
    namePlaceholder: "例如：数据库访问",
    duration: "启用时长",
    permanent: "永久",
    timed: "限时",
    hours: "小时",
    loopback: "本机",
    public: "全部网卡",
    save: "保存配置",
    cancel: "取消",
    start: "启用",
    stop: "停止",
    renew: "重新计时",
    renewHelp: "重新启用，按配置时长重新计算到期时间。现有连接会关闭。",
    remove: "删除",
    removeConfirm: "删除这条隧道？运行中的连接会立即关闭。",
    stopConfirm: "停止这条隧道？现有连接会立即关闭，配置仍会保留。",
    empty: "还没有隧道配置",
    emptyHelp: "先选择接收连接的入口机器，再选择访问目标服务的出口机器。",
    expires: "停止时间",
    connections: "当前连接",
    enableOnSave: "保存后立即启用",
    restricted: "组织管理员可以配置和维护隧道。",
    missing: "机器已删除",
    editHelp: "保存后会断开现有连接。",
    flow: "网络流向",
    refresh: "刷新",
    total: "条配置",
    status: "状态",
    machineRequired: "请选择机器",
    enabled: "已启用",
    unavailable: "未建立监听",
    traffic: "统计",
    totalTraffic: "累计流量",
    opened: "累计连接",
    peak: "峰值并发",
    p2p: "直连",
    mixed: "部分直连",
    negotiating: "中转 · 尝试直连",
    relayPath: "中转",
    portRules: "端口规则",
    portRulesHint: "每条规则独立监听端口、目标地址与出口。",
    rulesUnit: "条规则",
    addRule: "添加规则",
    portEnd: "结束端口",
    portEndPlaceholder: "留空=单端口",
    rangeHint: "端口区间内每个连接共用该规则的出口；IPv6 地址池按连接分配源地址。",
    rangeInvalid: "结束端口需大于起始端口，且区间不超过 1024 个端口。",
    removeRule: "删除规则",
    moveUp: "上移规则",
    moveDown: "下移规则",
    ruleName: "规则名称",
    rulePlaceholder: "可选，例如：MySQL",
    ruleProtocol: "监听协议",
    ruleProtocolAuto: "自动识别",
    ruleProtocolTcp: "TCP 透传",
    ruleProtocolHttp: "HTTP 代理",
    ruleProtocolSocks: "SOCKS 代理",
    ruleProtocolHelp:
      "同一端口可用 curl -x http:// 或 socks5:// 访问，自动识别协议。",
    invalidRule: "请补全每条规则的端口、目标地址与传输协议。",
    ...egressCopy["zh-CN"],
  },
  en: {
    creator: "Created by",
    temporary: "Temporary tunnel",
    managed: "Persistent configuration",
    source: "Source address",
    localUnknown: "Client · listener unknown",
    remoteUnknown: "Destination unknown",
    sshClient: "Client",
    temporaryHelp: "Ends when the client disconnects. Stop here when needed.",
    title: "Tunnels",
    unknown: "Offline · unconfirmed",
    create: "New tunnel",
    edit: "Edit tunnel",
    search: "Search tunnels or destinations",
    all: "All statuses",
    running: "Running",
    starting: "Connecting",
    error: "Connection failed · retrying",
    stopped: "Stopped",
    expired: "Expired",
    entry: "Entry",
    exit: "Exit",
    destination: "Destination service",
    bastion: "Server",
    relay: "Relay",
    access: "Client",
    direct: "Direct connection",
    listen: "Listen address",
    listenHint:
      "Binds on the entry machine. 127.0.0.1 allows local access only.",
    destinationHint:
      "Reached from the exit machine; 127.0.0.1 means that machine itself.",
    port: "Listen port",
    portForward: "Port forwarding",
    protocols: "Transport protocols",
    selectProtocol: "Select TCP or UDP (or both).",
    udpSSH: "UDP requires server or Agent endpoints; SSH machines support TCP only.",
    targetHost: "Destination address",
    targetPort: "Destination port",
    entryMachine: "Entry machine",
    exitMachine: "Exit machine",
    name: "Tunnel name",
    namePlaceholder: "e.g. Database access",
    duration: "Runtime",
    permanent: "Permanent",
    timed: "Timed",
    hours: "hours",
    loopback: "Local",
    public: "All interfaces",
    save: "Save configuration",
    cancel: "Cancel",
    start: "Enable",
    stop: "Stop",
    renew: "Reset timer",
    renewHelp:
      "Re-enable for the configured duration. Existing connections will close.",
    remove: "Delete",
    removeConfirm:
      "Delete this tunnel? Active connections will close immediately.",
    stopConfirm:
      "Stop this tunnel? Existing connections will close; the configuration is kept.",
    empty: "No tunnels yet",
    emptyHelp:
      "Choose the machine accepting connections, then the machine reaching the destination service.",
    expires: "Stops at",
    connections: "Active connections",
    enableOnSave: "Enable after saving",
    restricted: "Organization admins can configure and maintain tunnels.",
    missing: "Deleted machine",
    editHelp: "Saving closes active connections.",
    flow: "Network flow",
    refresh: "Refresh",
    total: "configurations",
    status: "Status",
    machineRequired: "Select a machine",
    enabled: "Enabled",
    unavailable: "Listener not established",
    traffic: "Statistics",
    totalTraffic: "Total traffic",
    opened: "Total connections",
    peak: "Peak concurrency",
    p2p: "Direct",
    mixed: "Partially direct",
    negotiating: "Relay · trying direct",
    relayPath: "Relay",
    portRules: "Port rules",
    portRulesHint: "Each rule listens on its own port with its own destination and egress.",
    rulesUnit: "rules",
    addRule: "Add rule",
    portEnd: "End port",
    portEndPlaceholder: "blank = single port",
    rangeHint:
      "Every connection in the range shares this rule's egress; an IPv6 pool allocates a source address per connection.",
    rangeInvalid:
      "The end port must exceed the start port and a range may cover at most 1024 ports.",
    removeRule: "Remove rule",
    moveUp: "Move rule up",
    moveDown: "Move rule down",
    ruleName: "Rule name",
    rulePlaceholder: "Optional, e.g. MySQL",
    ruleProtocol: "Listen protocol",
    ruleProtocolAuto: "Auto-detect",
    ruleProtocolTcp: "Raw TCP",
    ruleProtocolHttp: "HTTP proxy",
    ruleProtocolSocks: "SOCKS proxy",
    ruleProtocolHelp:
      "One port answers curl -x http:// or socks5://; auto-detect sniffs the protocol.",
    invalidRule: "Complete the port, destination and transports of every rule.",
    ...egressCopy.en,
  },
};
type Words = (typeof copy)["en"];
const blank: TunnelConfig = {
  name: "",
  entry_target_id: "",
  listen_host: "0.0.0.0",
  listen_port: 8080,
  exit_target_id: "",
  destination_host: "127.0.0.1",
  destination_port: 80,
  duration_seconds: 0,
};
const endpoint = (host: string, port: number) =>
  `${host.includes(":") ? `[${host}]` : host}:${port}`;

function Flow({
  config,
  targets,
  words: w,
  transport,
  editor,
}: {
  config: TunnelConfig;
  targets: Target[];
  words: Words;
  transport?: Tunnel["transport"];
  editor?: {
    data: ConsoleData;
    onChange: (patch: Partial<TunnelConfig>) => void;
  };
}) {
  const machine = (id: string) =>
    id ? targets.find((t) => t.id === id)?.name || w.missing : w.bastion;
  const machinePicker = (side: "entry" | "exit") => (
    <div className="field">
      <span>{side === "entry" ? w.entryMachine : w.exitMachine}</span>
      <MachinePicker
        targets={targets}
        folders={editor!.data.targetFolders}
        currentTargetID={
          side === "entry" ? config.entry_target_id : config.exit_target_id
        }
        label={side === "entry" ? w.entryMachine : w.exitMachine}
        variant="field"
        additionalOptions={
          editor!.data.user.is_system_admin
            ? [{ value: "", label: w.bastion }]
            : []
        }
        onOpenTarget={(id) =>
          editor!.onChange(
            side === "entry" ? { entry_target_id: id } : { exit_target_id: id },
          )
        }
      />
    </div>
  );
  const heading = (step: number, label: string, icon: React.ReactNode) => (
    <div className="tunnel-node-label">
      <span className="tunnel-step">{step}</span>
      {icon}
      <strong>{label}</strong>
    </div>
  );
  const routeLabel =
    transport === "direct" ? w.p2p : transport === "mixed" ? w.mixed : w.relay;
  const rules = portRulesOf(config);
  return (
    <div
      className={`tunnel-flow ${editor ? "tunnel-flow-editor" : ""}`}
      aria-label={w.flow}
    >
      <div className="tunnel-flow-nodes">
        <div className="tunnel-node entry-node">
          {heading(1, w.entry, <Server />)}
          {editor ? (
            <>
              {machinePicker("entry")}
              <span className="pill">
                {rules.length} {w.rulesUnit}
              </span>
            </>
          ) : (
            <>
              <strong>{machine(config.entry_target_id)}</strong>
              <span className="pill">
                {rules.length} {w.rulesUnit}
              </span>
            </>
          )}
        </div>
        <FlowArrow label={editor ? undefined : routeLabel} />
        <div className="tunnel-node exit-node">
          {heading(2, w.exit, <Network />)}
          {editor ? (
            machinePicker("exit")
          ) : (
            <>
              <strong>{machine(config.exit_target_id)}</strong>
            </>
          )}
          {editor && (
            <div className="tunnel-exit-symbol" aria-hidden="true">
              <Network />
              <ArrowRight />
            </div>
          )}
        </div>
        <FlowArrow />
        <div className="tunnel-node destination-node">
          {heading(3, w.destination, <Globe />)}
          {editor ? (
            <span className="tunnel-help">{w.portRulesHint}</span>
          ) : (
            <>
              <strong>
                {rules.length === 1
                  ? endpoint(rules[0].destination_host, rules[0].destination_port)
                  : `${rules.length} ${w.rulesUnit}`}
              </strong>
            </>
          )}
        </div>
      </div>
      {!editor && (
        <div className="port-rule-list">
          {rules.map((rule) => (
            <div className="port-rule-row" key={rule.id}>
              <code>{rule.listen_host || "0.0.0.0"}:{portRangeLabel(rule)}</code>
              <ArrowRight size={14} aria-hidden="true" />
              <span className="port-rule-tags">
                <Tag>
                  {(rule.transports?.length ? rule.transports : ["tcp"])
                    .map((p) => p.toUpperCase())
                    .join(" + ")}
                </Tag>
                {rule.protocol && rule.protocol !== "auto" && (
                  <Tag color="geekblue">{protocolLabel(rule.protocol, w)}</Tag>
                )}
              </span>
              <code>{endpoint(rule.destination_host, rule.destination_port)}</code>
              <span className="pill">{egressSummary(rule.egress, w)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// protocolLabel names an explicitly configured listen protocol.
function protocolLabel(protocol: PortRule["protocol"], w: Words): string {
  if (protocol === "tcp") return w.ruleProtocolTcp;
  if (protocol === "http") return w.ruleProtocolHttp;
  if (protocol === "socks") return w.ruleProtocolSocks;
  return w.ruleProtocolAuto;
}

// savePortRules omits an auto protocol so an old configuration keeps exactly
// the bytes it had: absent means auto on the server as well.
function savePortRules(rules: PortRule[]): PortRule[] {
  return rules.map((rule) => {
    if (rule.protocol && rule.protocol !== "auto") return rule;
    const next = { ...rule };
    delete next.protocol;
    return next;
  });
}

// portRangeLabel renders 8080 or 10000-11000 for a rule.
function portRangeLabel(rule: PortRule): string {
  const end = rule.listen_port_end ?? 0;
  return end > rule.listen_port ? `${rule.listen_port}-${end}` : String(rule.listen_port);
}

// portRangeProblem rejects an inverted or oversized range before saving.
function portRangeProblem(rule: PortRule): boolean {
  const end = rule.listen_port_end ?? 0;
  if (!end) return false;
  return end <= rule.listen_port || end - rule.listen_port + 1 > 1024;
}

function ruleID() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `rule-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

function newPortRule(): PortRule {
  return {
    id: ruleID(),
    listen_host: "0.0.0.0",
    listen_port: 8080,
    transports: ["tcp"],
    destination_host: "127.0.0.1",
    destination_port: 80,
  };
}

function portRulesOf(config: TunnelConfig): PortRule[] {
  if (config.port_rules?.length) return config.port_rules;
  return [
    {
      id: "legacy",
      listen_host: config.listen_host || "0.0.0.0",
      listen_port: config.listen_port,
      transports: config.transports?.length ? config.transports : ["tcp"],
      destination_host: config.destination_host,
      destination_port: config.destination_port,
    },
  ];
}

function hydratePortRules(config: TunnelConfig): PortRule[] {
  return portRulesOf(config).map((rule) => ({
    id: rule.id || ruleID(),
    name: rule.name,
    listen_host: rule.listen_host || "0.0.0.0",
    listen_port: rule.listen_port || 8080,
    listen_port_end: rule.listen_port_end,
    transports: rule.transports?.length ? rule.transports : ["tcp"],
    protocol: rule.protocol && rule.protocol !== "auto" ? rule.protocol : undefined,
    destination_host: rule.destination_host || "127.0.0.1",
    destination_port: rule.destination_port || 80,
    egress: rule.egress,
  }));
}

function PortRulesEditor({
  config,
  words: w,
  onChange,
}: {
  config: TunnelConfig;
  words: Words;
  onChange: (patch: Partial<TunnelConfig>) => void;
}) {
  const rules = config.port_rules ?? [];
  const apply = (next: PortRule[]) => {
    const transports = Array.from(
      new Set(next.flatMap((rule) => rule.transports ?? [])),
    ) as ("tcp" | "udp")[];
    const first = next[0];
    onChange({
      port_rules: next,
      transports: transports.length ? transports : ["tcp"],
      listen_host: first?.listen_host || "0.0.0.0",
      listen_port: first?.listen_port || 8080,
    });
  };
  const update = (index: number, patch: Partial<PortRule>) =>
    apply(rules.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)));
  const move = (index: number, delta: number) => {
    const target = index + delta;
    if (target < 0 || target >= rules.length) return;
    const next = [...rules];
    [next[index], next[target]] = [next[target], next[index]];
    apply(next);
  };
  return (
    <div className="port-rules-editor">
      <div className="port-rules-head">
        <strong>{w.portRules}</strong>
        <span className="pill">
          {rules.length} {w.rulesUnit}
        </span>
      </div>
      <span className="tunnel-help">{w.rangeHint}</span>
      <span className="tunnel-help">{w.ruleProtocolHelp}</span>
      {rules.map((rule, index) => (
        <Card
          key={rule.id}
          size="small"
          className="port-rule-card"
          title={
            <>
              <Tag color="blue">#{index + 1}</Tag>
              {rule.name ||
                `${rule.listen_host || "0.0.0.0"}:${portRangeLabel(rule)}`}
            </>
          }
          extra={
            <div className="port-rule-actions">
              <Button
                size="small"
                aria-label={w.moveUp}
                disabled={index === 0}
                onClick={() => move(index, -1)}
              >
                ↑
              </Button>
              <Button
                size="small"
                aria-label={w.moveDown}
                disabled={index === rules.length - 1}
                onClick={() => move(index, 1)}
              >
                ↓
              </Button>
              <Button
                danger
                type="text"
                aria-label={w.removeRule}
                icon={<Trash2 size={14} />}
                disabled={rules.length === 1}
                onClick={() => apply(rules.filter((_, i) => i !== index))}
              />
            </div>
          }
        >
          <div className="port-rule-grid">
            <label className="field">
              <span>{w.listen}</span>
              <Input
                aria-label={w.listen}
                value={rule.listen_host || ""}
                placeholder="0.0.0.0"
                onChange={(e) => update(index, { listen_host: e.target.value })}
              />
            </label>
            <label className="field">
              <span>{w.port}</span>
              <InputNumber
                aria-label={w.port}
                min={1}
                max={65535}
                value={rule.listen_port || null}
                onChange={(v) => update(index, { listen_port: Number(v) || 0 })}
              />
            </label>
            <label className="field">
              <span>{w.portEnd}</span>
              <InputNumber
                aria-label={w.portEnd}
                min={1}
                max={65535}
                value={rule.listen_port_end || null}
                placeholder={w.portEndPlaceholder}
                onChange={(v) =>
                  update(index, {
                    listen_port_end:
                      v && Number(v) > rule.listen_port ? Number(v) : undefined,
                  })
                }
              />
            </label>
            <div className="field">
              <span>{w.protocols}</span>
              <Checkbox.Group
                aria-label={w.protocols}
                options={["tcp", "udp"].map((value) => ({ label: value.toUpperCase(), value }))}
                value={rule.transports}
                onChange={(values) => update(index, { transports: values as ("tcp" | "udp")[] })}
              />
            </div>
            <label className="field">
              <span>{w.ruleProtocol}</span>
              <Select
                aria-label={w.ruleProtocol}
                value={rule.protocol ?? "auto"}
                options={[
                  { value: "auto", label: w.ruleProtocolAuto },
                  { value: "tcp", label: w.ruleProtocolTcp },
                  { value: "http", label: w.ruleProtocolHttp },
                  { value: "socks", label: w.ruleProtocolSocks },
                ]}
                onChange={(value) =>
                  update(index, {
                    protocol: value === "auto" ? undefined : value,
                  })
                }
              />
            </label>
            <label className="field">
              <span>{w.targetHost}</span>
              <Input
                aria-label={w.targetHost}
                value={rule.destination_host}
                onChange={(e) => update(index, { destination_host: e.target.value })}
              />
            </label>
            <label className="field">
              <span>{w.targetPort}</span>
              <InputNumber
                aria-label={w.targetPort}
                min={1}
                max={65535}
                value={rule.destination_port || null}
                onChange={(v) => update(index, { destination_port: Number(v) || 1 })}
              />
            </label>
          </div>
          <label className="field">
            <span>{w.ruleName}</span>
            <Input
              maxLength={128}
              value={rule.name || ""}
              placeholder={w.rulePlaceholder}
              onChange={(e) => update(index, { name: e.target.value })}
            />
          </label>
          <EgressEditor
            value={rule.egress}
            exitTargetID={config.exit_target_id}
            onChange={(egress) => update(index, { egress })}
          />
        </Card>
      ))}
      <Button
        type="dashed"
        block
        icon={<Plus size={15} />}
        disabled={rules.length >= 64}
        onClick={() => apply([...rules, newPortRule()])}
      >
        {w.addRule}
      </Button>
    </div>
  );
}

function FlowArrow({ label }: { label?: string }) {
  return (
    <div className="tunnel-flow-arrow">
      {label && <span>{label}</span>}
      <svg viewBox="0 0 80 32" aria-label="→">
        <path d="M2 16h72m-10-9 10 9-10 9" />
      </svg>
    </div>
  );
}

export function TunnelsPage({ data }: { data: ConsoleData }) {
  const { locale } = useI18n();
  const { theme } = useTheme();
  const w = copy[locale];
  const trafficWords = tunnelTrafficCopy[locale];
  const qc = useQueryClient();
  const allowed =
    data.user.is_system_admin ||
    ["owner", "admin"].includes(data.activeOrg.role || "");
  const [editing, setEditing] = useState<Tunnel | "new" | null>(null);
  const [confirmation, setConfirmation] = useState<{
    tunnel: Tunnel;
    action: "stop" | "delete" | "enable";
  } | null>(null);
  const [traffic, setTraffic] = useState<Tunnel | null>(null);
 const [protocolTab, setProtocolTab] = useState<"port" | "http">("port");
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  const key = ["tunnels", data.activeOrg.id];
  const tunnels = useQuery({
    queryKey: key,
    queryFn: () => api.tunnels(data.activeOrg.id),
    enabled: allowed,
    refetchInterval: 2000,
  });
  const action = useMutation({
    mutationFn: async ({
      tunnel,
      action,
    }: {
      tunnel: Tunnel;
      action: "stop" | "delete" | "enable";
    }) => {
      if (action === "delete") await api.deleteTunnel(tunnel.id);
      else await api.tunnelAction(tunnel.id, action);
    },
    onSuccess: async () => {
      setConfirmation(null);
      await qc.invalidateQueries({ queryKey: key });
    },
  });
  const all = (tunnels.data?.tunnels || []).filter(t => (t.temporary ? "port" : t.protocol) === protocolTab);
  const items = all.filter(
    (t) =>
      (filter === "all" || t.status === filter) &&
      `${t.name} ${t.destination_host} ${t.listen_host} ${t.http?.servers.flatMap(s=>s.hosts).join(" ")||""}`
        .toLowerCase()
        .includes(query.toLowerCase()),
  );
  return (
    <ConfigProvider
      theme={{
        algorithm:
          theme === "dark"
            ? antdTheme.darkAlgorithm
            : antdTheme.defaultAlgorithm,
        token: {
          colorBgContainer: theme === "dark" ? "#111827" : "#fff",
          colorBgElevated: theme === "dark" ? "#111827" : "#fff",
          colorText: theme === "dark" ? "#f2f4f7" : "#182230",
          colorBorder: theme === "dark" ? "#344054" : "#e4e7ec",
          controlHeight: 40,
          fontSize: 13,
          borderRadius: 8,
        },
      }}
    >
      <div className="tunnels-page">
        <section className="resource-head">
          <Button
            type="primary"
            icon={<Plus size={16} />}
            disabled={!allowed}
            onClick={() => setEditing("new")}
          >
            {w.create}
          </Button>
        </section>
<Tabs activeKey={protocolTab} onChange={v=>{setProtocolTab(v as "port"|"http");setQuery("");}} items={[{key:"port",label:w.portForward},{key:"http",label:"HTTP "+(locale==="zh-CN"?"转发":"forwarding")}]}/>
        {!allowed ? (
          <p>{w.restricted}</p>
        ) : (
          <>
            <div className="tunnel-toolbar">
              <Input
                prefix={<Search size={16} />}
                placeholder={w.search}
                aria-label={w.search}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
              <Select
                aria-label={w.status}
                value={filter}
                onChange={setFilter}
                options={[
                  "all",
                  "running",
                  "starting",
                  "error",
                  "stopped",
                  "expired",
                  "unknown",
                ].map((value) => ({ value, label: w[value as keyof Words] }))}
              />
              <span>
                {all.length} {w.total}
              </span>
              <Tooltip title={w.refresh}>
                <Button
                  aria-label={w.refresh}
                  icon={<RefreshCw size={16} />}
                  onClick={() => void tunnels.refetch()}
                />
              </Tooltip>
            </div>
            <ErrorMessage error={tunnels.error || action.error} />
            {items.length === 0 && !tunnels.isLoading && (
              <div className="tunnel-empty">
                <Network size={40} />
                <h3>{all.length ? w.search : w.empty}</h3>
                <p>{w.emptyHelp}</p>
                {!all.length && (
                  <Button type="primary" onClick={() => setEditing("new")}>
                    {w.create}
                  </Button>
                )}
              </div>
            )}
            {items.map((t) => (
              <article className="tunnel-card" key={t.id}>
                <div className="tunnel-card-head">
                  <div>
                    <h3>{t.temporary ? w.temporary : t.name}</h3>
                    <TunnelPathStatus tunnel={t} targets={data.targets} />
                  </div>
                  <div className="tunnel-card-actions">
                    <Button
                      disabled={action.isPending}
                      icon={
                        t.enabled ? <Square size={14} /> : <Play size={14} />
                      }
                      onClick={() =>
                        t.enabled
                          ? setConfirmation({ tunnel: t, action: "stop" })
                          : action.mutate({ tunnel: t, action: "enable" })
                      }
                    >
                      {t.enabled ? w.stop : w.start}
                    </Button>
                    <Tooltip title={w.edit}>
                      <Button
                        aria-label={w.edit}
                        disabled={t.temporary}
                        icon={<Pencil size={15} />}
                        onClick={() => setEditing(t)}
                      />
                    </Tooltip>
                    <Tooltip title={w.remove}>
                      <Button
                        danger
                        aria-label={w.remove}
                        disabled={t.temporary}
                        icon={<Trash2 size={15} />}
                        onClick={() =>
                          setConfirmation({ tunnel: t, action: "delete" })
                        }
                      />
                    </Tooltip>
                  </div>
                </div>
                <div className="tunnel-owner-note">
                  <Tooltip title={t.temporary ? w.temporaryHelp : undefined}>
                    <Tag>{t.temporary ? w.temporary : w.managed}</Tag>
                  </Tooltip>
                  <span>
                    {w.creator}: {t.creator_name || t.created_by || "—"}
                  </span>
                  {t.remote_address && (
                    <span title={t.public_key_fingerprint}>
                      {w.source}: {t.remote_address}
                    </span>
                  )}
                </div>
                {t.protocol === "http" ? <HTTPForwardSummary config={t}/> : t.temporary ? (
                  <div className="tunnel-flow" aria-label={w.flow}>
                    <div className="tunnel-flow-nodes">
                      <div className="tunnel-node entry-node">
                        <small>{w.entry}</small>
                        <strong>
                          {t.forward_type === "local"
                            ? w.localUnknown
                            : w.bastion}
                        </strong>
                        <code>
                          {t.forward_type === "remote"
                            ? t.listen_address
                            : t.remote_address}
                        </code>
                      </div>
                      <div className="tunnel-flow-arrow">
                        <ArrowRight aria-label="→" />
                      </div>
                      <div className="tunnel-node exit-node">
                        <small>{w.exit}</small>
                        <strong>
                          {t.forward_type === "remote"
                            ? w.sshClient
                            : data.targets.find(
                                (x) => x.id === t.exit_target_id,
                              )?.name || w.missing}
                        </strong>
                      </div>
                      <div className="tunnel-flow-arrow destination-arrow">
                        <ArrowRight aria-label="→" />
                      </div>
                      <div className="tunnel-node destination-node">
                        <small>{w.destination}</small>
                        <strong>
                          {t.forward_type === "local"
                            ? t.destination_host
                            : w.remoteUnknown}
                        </strong>
                        {t.forward_type === "local" && (
                          <code>{t.destination_port}</code>
                        )}
                      </div>
                    </div>
                  </div>
                ) : (
                  <Flow
                    config={t}
                    targets={data.targets}
                    words={w}
                    transport={t.transport}
                  />
                )}
                <div className="tunnel-card-metrics">
                  <div>
                    <small>{w.totalTraffic}</small>
                    <strong>
                      {formatBytes(
                        (t.traffic?.relay_up || 0) +
                          (t.traffic?.relay_down || 0) +
                          (t.traffic?.direct_up || 0) +
                          (t.traffic?.direct_down || 0),
                      )}
                    </strong>
                    <span>
                      {trafficWords.up} ({trafficWords.upDirection}):{" "}
                      {formatBytes(
                        (t.traffic?.relay_up || 0) +
                          (t.traffic?.direct_up || 0),
                      )}
                    </span>
                    <span>
                      {trafficWords.down} ({trafficWords.downDirection}):{" "}
                      {formatBytes(
                        (t.traffic?.relay_down || 0) +
                          (t.traffic?.direct_down || 0),
                      )}
                    </span>
                  </div>
                  <div>
                    <small>{w.connections}</small>
                    <strong>{t.connections}</strong>
                    <span>
                      {w.p2p}: {t.direct_connections || 0}
                    </span>
                  </div>
                  <div>
                    <small>{w.opened}</small>
                    <strong>{t.traffic?.connections_opened || 0}</strong>
                    <span>
                      {w.peak}: {t.traffic?.peak_connections || 0}
                    </span>
                  </div>
                  <Button
                    icon={<ChartNoAxesCombined size={16} />}
                    onClick={() => setTraffic(t)}
                  >
                    {w.traffic}
                  </Button>
                </div>
                <div className="tunnel-card-foot">
                  <span>
                    <Clock3 size={14} />
                    {t.expires_at
                      ? `${w.expires}: ${new Date(t.expires_at).toLocaleString(locale)}`
                      : t.temporary
                        ? w.temporary
                        : w.permanent}
                  </span>
                  <span>
                    {w.connections}: {t.connections}
                  </span>
                  {t.enabled && t.duration_seconds > 0 && (
                    <Tooltip title={w.renewHelp}>
                      <Button
                        size="small"
                        type="text"
                        onClick={() =>
                          setConfirmation({ tunnel: t, action: "enable" })
                        }
                      >
                        {w.renew}
                      </Button>
                    </Tooltip>
                  )}
                  {t.listen_address && t.status === "running" && (
                    <code>
                      {w.listen}: {t.listen_address}
                    </code>
                  )}
                </div>
                <TunnelErrorNotice tunnel={t} />
              </article>
            ))}
          </>
        )}
        {traffic && (traffic.protocol ? <ForwardLogsDialog tunnel={all.find(t=>t.id===traffic.id)||traffic} onClose={()=>setTraffic(null)}/> :
          <TunnelTrafficDialog
            tunnel={all.find((t) => t.id === traffic.id) || traffic}
            onClose={() => setTraffic(null)}
          />
        )}
        {editing && (
          <TunnelEditor
            key={`${data.activeOrg.id}-${editing === "new" ? "new" : editing.id}`}
            data={data}
            tunnel={editing === "new" ? null : editing}
            words={w}
            initialProtocol={protocolTab}
            onClose={() => setEditing(null)}
            onSaved={() => qc.invalidateQueries({ queryKey: key })}
          />
        )}
        {confirmation && (
          <ConfirmDialog
            title={confirmation.tunnel.name}
            confirmLabel={
              confirmation.action === "delete"
                ? w.remove
                : confirmation.action === "stop"
                  ? w.stop
                  : w.renew
            }
            danger={confirmation.action !== "enable"}
            body={
              confirmation.action === "delete"
                ? w.removeConfirm
                : confirmation.action === "stop"
                  ? w.stopConfirm
                  : w.renewHelp
            }
            onClose={() => setConfirmation(null)}
            onConfirm={() => action.mutate(confirmation)}
          />
        )}
      </div>
    </ConfigProvider>
  );
}

function TunnelEditor({
  initialProtocol,
  data,
  tunnel,
  words: w,
  onClose,
  onSaved,
}: {
  data: ConsoleData;
  tunnel: Tunnel | null;
  initialProtocol: "port" | "http";
  words: Words;
  onClose: () => void;
  onSaved: () => Promise<unknown>;
}) {
  const firstMachine = data.user.is_system_admin
    ? ""
    : data.targets[0]?.id || "__select__";
  const [config, setConfig] = useState<TunnelConfig>(() => {
    if (!tunnel) {
      const rule = newPortRule();
      return {
        ...blank,
        protocol: initialProtocol,
        transports: rule.transports,
        port_rules: [rule],
        listen_host: rule.listen_host || "0.0.0.0",
        listen_port: rule.listen_port,
        destination_host: rule.destination_host,
        destination_port: rule.destination_port,
        http: initialProtocol === "http" ? newHTTPConfig(firstMachine) : undefined,
        entry_target_id: firstMachine,
        exit_target_id: firstMachine,
      };
    }
    const rules = hydratePortRules(tunnel);
    return {
      protocol: tunnel.protocol,
      transports: tunnel.transports,
      http: tunnel.http,
      port_rules: rules,
      name: tunnel.name,
      entry_target_id: tunnel.entry_target_id,
      listen_host: rules[0]?.listen_host || tunnel.listen_host,
      listen_port: rules[0]?.listen_port || tunnel.listen_port,
      exit_target_id: tunnel.exit_target_id,
      destination_host: rules[0]?.destination_host || tunnel.destination_host,
      destination_port: rules[0]?.destination_port || tunnel.destination_port,
      duration_seconds: tunnel.duration_seconds,
    };
  });
  const [savedID, setSavedID] = useState<string | null>(null);
  const [enable, setEnable] = useState(!tunnel);
  const [timed, setTimed] = useState(config.duration_seconds > 0);
  const set = <K extends keyof TunnelConfig>(key: K, value: TunnelConfig[K]) =>
    setConfig((c) => ({ ...c, [key]: value }));
  const rules = config.port_rules ?? [];
  const ruleTransports = Array.from(
    new Set(rules.flatMap((rule) => rule.transports ?? [])),
  ) as ("tcp" | "udp")[];
  const invalidProtocols = config.protocol === "port" && ruleTransports.length === 0;
  const invalidRules =
    config.protocol === "port" &&
    (rules.length === 0 ||
      rules.some(
        (rule) =>
          !rule.listen_port ||
          rule.listen_port < 1 ||
          rule.listen_port > 65535 ||
          !(rule.destination_host || "").trim() ||
          !rule.destination_port ||
          !(rule.transports ?? []).length,
      ));
  const rangeIssue =
    config.protocol === "port" && rules.some((rule) => portRangeProblem(rule))
      ? w.rangeInvalid
      : null;
  const egressIssue =
    config.protocol === "port"
      ? rules.map((rule) => egressProblem(rule.egress, w)).find(Boolean) || null
      : config.http?.servers
          .flatMap((server) => server.locations)
          .map((location) => egressProblem(location.egress, w))
          .find(Boolean) || null;
  const udpSSH = config.protocol === "port" && ruleTransports.includes("udp") &&
    [config.entry_target_id, config.exit_target_id].some(id => id && data.targets.find(t => t.id === id)?.target_type === "direct");
  const mutation = useMutation({
    mutationFn: async () => {
      if (invalidProtocols) throw new Error(w.selectProtocol);
      if (invalidRules) throw new Error(w.invalidRule);
      if (rangeIssue) throw new Error(rangeIssue);
      if (egressIssue) throw new Error(egressIssue);
      if (udpSSH) throw new Error(w.udpSSH);
      const first = rules[0];
      const body: TunnelConfig =
        config.protocol === "port"
          ? {
              ...config,
              transports: ruleTransports,
              port_rules: savePortRules(rules),
              listen_host: first?.listen_host || "0.0.0.0",
              listen_port: first?.listen_port || 8080,
              destination_host: first?.destination_host || "127.0.0.1",
              destination_port: first?.destination_port || 80,
              http: undefined,
            }
          : { ...config, transports: undefined, port_rules: undefined };
      const result =
        tunnel || savedID
          ? await api.updateTunnel(tunnel?.id || savedID!, body)
          : await api.createTunnel({
              ...body,
              organization_id: data.activeOrg.id,
            });
      setSavedID(result.tunnel.id);
      await onSaved();
      if (enable) {
        await api.tunnelAction(result.tunnel.id, "enable");
        await onSaved();
      }
      return result;
    },
    onSuccess: onClose,
  });
  return (
    <Modal
      title={tunnel ? w.edit : w.create}
      wide
      className="tunnel-editor"
      onClose={onClose}
      closeOnEscape={!mutation.isPending}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          mutation.mutate();
        }}
      >
        <label className="field tunnel-name">
          <span>{w.name}</span>
          <Input
            required
            maxLength={128}
            value={config.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder={w.namePlaceholder}
          />
        </label>
<div className="forward-editor-type"><Segmented value={config.protocol || "port"} items={[["port",w.portForward],["http","HTTP"]]} onChange={v=>{setConfig(c=>({...c,protocol:v as "port"|"http",transports:v === "port" ? c.transports ?? ["tcp"] : c.transports,http:c.http||newHTTPConfig(firstMachine)}));}}/></div>
        <div className="tunnel-preview-heading">
          <Network size={17} />
          <strong>{w.flow}</strong>
        </div>
        {config.protocol === "http" ? <HTTPForwardEditor data={data} config={config} onChange={patch=>setConfig(c=>({...c,...patch}))}/> : <><Flow
          config={config}
          targets={data.targets}
          words={w}
          editor={{
            data,
            onChange: (patch) => setConfig((c) => ({ ...c, ...patch })),
          }}
        />
        <PortRulesEditor
          config={config}
          words={w}
          onChange={(patch) => setConfig((c) => ({ ...c, ...patch }))}
        /></>}
        <div className="tunnel-duration">
          <div className="field">
            <span>
              <Clock3 size={14} />
              {w.duration}
            </span>
            <Segmented
              value={timed ? "timed" : "permanent"}
              items={[
                ["permanent", w.permanent],
                ["timed", w.timed],
              ]}
              onChange={(v) => {
                setTimed(v === "timed");
                set("duration_seconds", v === "timed" ? 3600 : 0);
              }}
            />
          </div>
          {timed && (
            <label className="field">
              <span>{w.hours}</span>
              <InputNumber
                aria-label={w.hours}
                min={1 / 60}
                max={8760}
                step={0.25}
                value={config.duration_seconds / 3600}
                onChange={(v) =>
                  set("duration_seconds", Math.round((v || 1) * 3600))
                }
              />
            </label>
          )}
        </div>
        {tunnel?.enabled && <p className="tunnel-edit-warning">{w.editHelp}</p>}
        {!tunnel && (
          <label className="tunnel-enable">
            <Switch
              checked={enable}
              onChange={setEnable}
              aria-label={w.enableOnSave}
            />
            <span>{w.enableOnSave}</span>
          </label>
        )}
        {config.protocol === "port" && (invalidProtocols || invalidRules || rangeIssue) && (
          <p role="alert" className="tunnel-edit-warning">
            {rangeIssue || (invalidRules ? w.invalidRule : w.selectProtocol)}
          </p>
        )}
        {egressIssue && <p role="alert" className="tunnel-edit-warning">{egressIssue}</p>}
        {udpSSH && <p role="alert" className="tunnel-edit-warning">{w.udpSSH}</p>}
        <ErrorMessage error={mutation.error} />
        <div className="form-actions">
          <Button onClick={onClose} disabled={mutation.isPending}>
            {w.cancel}
          </Button>
          <Button
            type="primary"
            htmlType="submit"
            loading={mutation.isPending}
            disabled={
              invalidProtocols || invalidRules || Boolean(rangeIssue) || Boolean(egressIssue) || udpSSH || config.entry_target_id === "__select__" ||
              (config.protocol === "http" ? config.http?.servers.some(s=>s.locations.some(l=>l.exit_target_id === "__select__")) : config.exit_target_id === "__select__")
            }
          >
            {w.save}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
