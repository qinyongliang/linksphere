import { useQuery } from "@tanstack/react-query";
import { Input, InputNumber, Select } from "antd";
import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { useI18n } from "../i18n";
import type { EgressFamily, EgressSpec, NetInterface } from "../types";
import "./egress.css";

const enEgress = {
  egress: "Egress",
  hostEgress: "Host egress",
  pinnedNIC: "Pinned NIC",
  ipv6Pool: "IPv6 pool",
  upstreamProxy: "Upstream proxy",
  family: "Address family",
  familyAuto: "v4+v6 (auto)",
  familyIPv4: "IPv4 only",
  familyIPv6: "IPv6 only",
  familyBoth: "Both",
  nic: "Egress interface",
  nicPlaceholder: "Select an interface",
  ipv6Mode: "IPv6 source",
  ipv6Host: "Host default",
  ipv6Interface: "Interface address",
  ipv6PoolMode: "Address pool",
  poolStart: "Pool start",
  poolStartPlaceholder: "2001:db8::100",
  poolSize: "Pool size",
  poolMode: "Allocation",
  rotate: "Random per connection",
  sticky: "Sequential from previous",
  proxyURL: "Proxy URL",
  proxyPlaceholder: "socks5://user:pass@192.0.2.10:1080",
  loading: "Reading exit interfaces…",
  unsupported: "This exit cannot pin a NIC: SSH exits have no interface list.",
  noNIC: "No interface with a global IPv6 address.",
  netFailed: "Could not read the exit network information.",
  poolInvalid: "Pool start must be an IPv6 address.",
  poolSizeInvalid: "Pool size must be between 1 and 65536.",
  proxyInvalid: "Proxy URL must start with socks5://, socks5h://, http:// or https://.",
  interfaceRequired: "Select an egress interface.",
  noExit: "Select an exit machine first.",
};

const zhEgress: typeof enEgress = {
  egress: "出口",
  hostEgress: "主机出口",
  pinnedNIC: "指定网卡",
  ipv6Pool: "IPv6 地址池",
  upstreamProxy: "上游代理",
  family: "地址族",
  familyAuto: "v4+v6（自动）",
  familyIPv4: "仅 IPv4",
  familyIPv6: "仅 IPv6",
  familyBoth: "双栈",
  nic: "出口网卡",
  nicPlaceholder: "选择网卡",
  ipv6Mode: "IPv6 来源",
  ipv6Host: "主机默认",
  ipv6Interface: "网卡地址",
  ipv6PoolMode: "地址池",
  poolStart: "池起始地址",
  poolStartPlaceholder: "2001:db8::100",
  poolSize: "池大小",
  poolMode: "分配方式",
  rotate: "每连接随机",
  sticky: "顺序分配",
  proxyURL: "代理地址",
  proxyPlaceholder: "socks5://user:pass@192.0.2.10:1080",
  loading: "正在读取出口网卡…",
  unsupported: "该出口无法指定网卡：SSH 出口没有网卡列表。",
  noNIC: "没有带全局 IPv6 地址的网卡。",
  netFailed: "无法读取出口网络信息。",
  poolInvalid: "池起始地址必须是 IPv6 地址。",
  poolSizeInvalid: "池大小需在 1 到 65536 之间。",
  proxyInvalid: "代理地址需以 socks5://、socks5h://、http:// 或 https:// 开头。",
  interfaceRequired: "请选择出口网卡。",
  noExit: "请先选择出口机器。",
};

export const egressCopy = { "zh-CN": zhEgress, en: enEgress };
export type EgressWords = typeof enEgress;

/** normalizeEgress fills the contract defaults so callers never branch on empty strings. */
export function normalizeEgress(spec?: EgressSpec): EgressSpec {
  return {
    mode: spec?.mode === "proxy" ? "proxy" : "direct",
    family: spec?.family ?? "auto",
    interface: spec?.interface?.trim() || undefined,
    ipv6_mode: spec?.ipv6_mode ?? "host",
    pool_start: spec?.pool_start?.trim() || undefined,
    pool_size: spec?.pool_size || 0,
    pool_mode: spec?.pool_mode === "sticky" ? "sticky" : "rotate",
    proxy_url: spec?.proxy_url?.trim() || undefined,
  };
}

/** isDefaultEgress matches the wire default: host routing table, dual stack. */
export function isDefaultEgress(spec?: EgressSpec): boolean {
  const n = normalizeEgress(spec);
  return n.mode === "direct" && n.family === "auto" && !n.interface && n.ipv6_mode === "host" && !n.pool_start && !n.proxy_url;
}

/** compactEgress drops fields that do not apply to the selected mode. */
export function compactEgress(spec: EgressSpec): EgressSpec {
  const n = normalizeEgress(spec);
  const out: EgressSpec = {
    mode: n.mode,
    family: n.family,
    ipv6_mode: n.mode === "proxy" ? "host" : n.ipv6_mode,
    pool_mode: n.pool_mode,
  };
  if (n.mode === "proxy") {
    out.proxy_url = n.proxy_url;
    return out;
  }
  if (out.ipv6_mode === "interface" && n.interface) out.interface = n.interface;
  if (out.ipv6_mode === "pool") {
    out.pool_start = n.pool_start;
    out.pool_size = n.pool_size;
  }
  return out;
}

export function egressProblem(spec: EgressSpec | undefined, w: EgressWords): string | null {
  const n = normalizeEgress(spec);
  if (n.mode === "proxy") {
    if (!n.proxy_url || !/^(socks5|socks5h|http|https):\/\/.+/i.test(n.proxy_url)) return w.proxyInvalid;
    return null;
  }
  if (n.ipv6_mode === "interface" && !n.interface) return w.interfaceRequired;
  if (n.ipv6_mode === "pool") {
    if (!n.pool_start || !n.pool_start.includes(":")) return w.poolInvalid;
    if (!n.pool_size || n.pool_size < 1 || n.pool_size > 65536) return w.poolSizeInvalid;
  }
  return null;
}

function familyLabel(family: EgressFamily, w: EgressWords) {
  if (family === "ipv4") return w.familyIPv4;
  if (family === "ipv6") return w.familyIPv6;
  if (family === "both") return w.familyBoth;
  return w.familyAuto;
}

function familyShort(family: EgressFamily) {
  if (family === "ipv4") return "IPv4";
  if (family === "ipv6") return "IPv6";
  if (family === "both") return "v4+v6";
  return "v4+v6";
}

/** egressSummary renders one line for a tunnel card or rule header. */
export function egressSummary(spec: EgressSpec | undefined, w: EgressWords): string {
  const n = normalizeEgress(spec);
  if (n.mode === "proxy") return `${w.upstreamProxy} ${n.proxy_url || "—"}`;
  if (n.ipv6_mode === "pool") {
    return `${w.ipv6Pool} ${n.pool_start || "—"} · ${n.pool_mode === "sticky" ? w.sticky : w.rotate}`;
  }
  if (n.ipv6_mode === "interface") return `${w.pinnedNIC} ${n.interface || "—"} · IPv6`;
  return `${w.hostEgress} ${familyShort(n.family)}`;
}

const globalIPv6 = (iface: NetInterface) =>
  (iface.addresses ?? []).find(
    (address) => address.family === "ipv6" && (!address.scope || address.scope === "global"),
  );

export function EgressEditor({
  value,
  exitTargetID,
  onChange,
}: {
  value?: EgressSpec;
  exitTargetID: string;
  onChange: (spec: EgressSpec | undefined) => void;
}) {
  const { locale } = useI18n();
  const w = egressCopy[locale];
  const external = useMemo(() => {
    const normalized = normalizeEgress(value);
    return isDefaultEgress(normalized) ? null : compactEgress(normalized);
  }, [value]);
  const externalKey = JSON.stringify(external);
  const [draft, setDraft] = useState<EgressSpec>(() => normalizeEgress(value));
  const lastEmitted = useRef(externalKey);

  useEffect(() => {
    if (externalKey === lastEmitted.current) return;
    lastEmitted.current = externalKey;
    setDraft(normalizeEgress(value));
  }, [externalKey, value]);

  const emit = (spec: EgressSpec) => {
    const next = isDefaultEgress(spec) ? undefined : compactEgress(spec);
    lastEmitted.current = JSON.stringify(next ?? null);
    onChange(next);
  };

  const update = (patch: Partial<EgressSpec>) => {
    const next = normalizeEgress({ ...draft, ...patch });
    setDraft(next);
    emit(next);
  };

  const net = useQuery({
    queryKey: ["target-net", exitTargetID],
    queryFn: () => api.targetNetInfo(exitTargetID),
    enabled: Boolean(exitTargetID),
    retry: false,
    staleTime: 60000,
  });

  const supported = Boolean(exitTargetID) && net.data?.supported === true;
  const interfaces = useMemo(
    () =>
      (net.data?.net_info?.interfaces ?? []).filter(
        (iface) => iface.up && !iface.loopback && Boolean(globalIPv6(iface)),
      ),
    [net.data],
  );

  const hint = !exitTargetID
    ? w.noExit
    : net.isError
      ? w.netFailed
      : net.isPending
        ? w.loading
        : net.data && !net.data.supported
          ? net.data.reason?.trim() || w.unsupported
          : supported && interfaces.length === 0
            ? w.noNIC
            : "";
  const problem = egressProblem(draft, w);
  // Advanced IPv6 controls stay hidden until IPv6 egress is actually chosen, so the
  // default rule is a one-line "host egress, v4+v6".
  const showIPv6 = draft.mode !== "proxy" && draft.family !== "ipv4" && (draft.family === "ipv6" || draft.ipv6_mode !== "host");
  const modeChoice =
    draft.mode === "proxy"
      ? "proxy"
      : draft.ipv6_mode === "pool"
        ? "pool"
        : draft.ipv6_mode === "interface"
          ? "interface"
          : "host";

  const setMode = (choice: string) => {
    const needsIPv6 = choice === "interface" || choice === "pool";
    const family: EgressFamily = needsIPv6 && draft.family === "ipv4" ? "both" : draft.family;
    if (choice === "proxy") {
      update({ mode: "proxy", family, ipv6_mode: "host", interface: undefined, pool_start: undefined, pool_size: 0 });
    } else if (choice === "interface") {
      update({ mode: "direct", family, ipv6_mode: "interface" });
    } else if (choice === "pool") {
      update({ mode: "direct", family, ipv6_mode: "pool", pool_size: draft.pool_size || 64, pool_mode: draft.pool_mode || "rotate" });
    } else {
      update({ mode: "direct", family, ipv6_mode: "host", interface: undefined, pool_start: undefined, pool_size: 0 });
    }
  };

  const setIPv6Mode = (mode: string) => {
    if (mode === "pool") {
      update({ mode: "direct", ipv6_mode: "pool", interface: undefined, pool_size: draft.pool_size || 64, pool_mode: draft.pool_mode || "rotate" });
    } else if (mode === "interface") {
      update({ mode: "direct", ipv6_mode: "interface", pool_start: undefined, pool_size: 0 });
    } else {
      update({ mode: "direct", ipv6_mode: "host", interface: undefined, pool_start: undefined, pool_size: 0 });
    }
  };

  const poolStartInvalid = draft.ipv6_mode === "pool" && Boolean(draft.pool_start) && !draft.pool_start?.includes(":");
  const proxyInvalid = draft.mode === "proxy" && Boolean(draft.proxy_url) && !/^(socks5|socks5h|http|https):\/\/.+/i.test(draft.proxy_url || "");

  return (
    <div className="egress-editor">
      <div className="egress-grid">
        <label className="field">
          <span>{w.egress}</span>
          <Select
            aria-label={w.egress}
            value={modeChoice}
            onChange={setMode}
            options={[
              { value: "host", label: w.hostEgress },
              { value: "interface", label: w.pinnedNIC, disabled: !supported },
              { value: "pool", label: w.ipv6Pool, disabled: !supported },
              { value: "proxy", label: w.upstreamProxy },
            ]}
          />
        </label>
        <label className="field">
          <span>{w.family}</span>
          <Select
            aria-label={w.family}
            value={draft.family}
            onChange={(family: EgressFamily) =>
              update(
                family === "ipv4" && draft.ipv6_mode !== "host"
                  ? { family, ipv6_mode: "host", interface: undefined, pool_start: undefined, pool_size: 0 }
                  : { family },
              )
            }
            options={[
              { value: "auto", label: familyLabel("auto", w) },
              { value: "ipv4", label: familyLabel("ipv4", w) },
              { value: "ipv6", label: familyLabel("ipv6", w) },
              { value: "both", label: familyLabel("both", w) },
            ]}
          />
        </label>
        {draft.mode === "proxy" && (
          <label className="field egress-wide">
            <span>{w.proxyURL}</span>
            <Input
              aria-label={w.proxyURL}
              value={draft.proxy_url || ""}
              placeholder={w.proxyPlaceholder}
              status={proxyInvalid ? "error" : undefined}
              onChange={(event) => update({ proxy_url: event.target.value })}
            />
          </label>
        )}
      </div>
      {showIPv6 && (
        <div className="egress-grid">
          <label className="field">
            <span>{w.nic}</span>
            <Select<string | undefined>
              aria-label={w.nic}
              allowClear
              value={draft.ipv6_mode === "interface" ? draft.interface : undefined}
              placeholder={w.nicPlaceholder}
              disabled={!supported || interfaces.length === 0}
              onChange={(name) =>
                update(
                  name
                    ? {
                        mode: "direct",
                        family: draft.family === "ipv4" ? "both" : draft.family,
                        ipv6_mode: "interface",
                        interface: name,
                        pool_start: undefined,
                        pool_size: 0,
                      }
                    : { mode: "direct", ipv6_mode: "host", interface: undefined },
                )
              }
              options={interfaces.map((iface) => {
                const address = globalIPv6(iface);
                return { value: iface.name, label: address ? `${iface.name} · ${address.address}` : iface.name };
              })}
            />
          </label>
          <label className="field">
            <span>{w.ipv6Mode}</span>
            <Select
              aria-label={w.ipv6Mode}
              value={draft.ipv6_mode}
              onChange={setIPv6Mode}
              options={[
                { value: "host", label: w.ipv6Host },
                { value: "interface", label: w.ipv6Interface, disabled: !supported },
                { value: "pool", label: w.ipv6PoolMode, disabled: !supported },
              ]}
            />
          </label>
          {draft.ipv6_mode === "pool" && (
            <>
              <label className="field">
                <span>{w.poolStart}</span>
                <Input
                  aria-label={w.poolStart}
                  value={draft.pool_start || ""}
                  placeholder={w.poolStartPlaceholder}
                  disabled={!supported}
                  status={poolStartInvalid ? "error" : undefined}
                  onChange={(event) => update({ pool_start: event.target.value })}
                />
              </label>
              <label className="field">
                <span>{w.poolSize}</span>
                <InputNumber
                  aria-label={w.poolSize}
                  min={1}
                  max={65536}
                  value={draft.pool_size || 64}
                  disabled={!supported}
                  onChange={(size) => update({ pool_size: Number(size) || 64 })}
                />
              </label>
              <label className="field">
                <span>{w.poolMode}</span>
                <Select
                  aria-label={w.poolMode}
                  value={draft.pool_mode}
                  disabled={!supported}
                  onChange={(pool_mode: "rotate" | "sticky") => update({ pool_mode })}
                  options={[
                    { value: "rotate", label: w.rotate },
                    { value: "sticky", label: w.sticky },
                  ]}
                />
              </label>
            </>
          )}
        </div>
      )}
      {hint && <p className="egress-hint">{hint}</p>}
      {problem && <p className="egress-hint danger">{problem}</p>}
    </div>
  );
}
