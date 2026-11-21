import { Button, Card, Checkbox, Input, InputNumber, Tooltip } from "antd";
import { ArrowRight, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import type { ConsoleData, PortRule, Target, TunnelConfig } from "../types";
import { EgressEditor } from "./EgressEditor";
import { MachinePicker } from "./MachinePicker";
import { Segmented } from "./ui";
import "./flow-lanes.css";

type Words = Record<string, string>;

export interface FlowLanesProps {
  data: ConsoleData;
  config: TunnelConfig;
  targets: Target[];
  words: Words;
  onChange: (patch: Partial<TunnelConfig>) => void;
}

function ruleID(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `rule-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

function newRule(port: number): PortRule {
  return {
    id: ruleID(),
    listen_host: "0.0.0.0",
    listen_port: port,
    transports: ["tcp"],
    // A fixed target defaults to this machine, the usual reason to forward.
    destination_host: "127.0.0.1",
    destination_port: 80,
    protocol: "tcp",
  };
}

function isRange(rule: PortRule): boolean {
  return Boolean(rule.listen_port_end && rule.listen_port_end > rule.listen_port);
}

/** portText renders one field as 8080 or 8080-8099. */
function portText(rule: PortRule): string {
  return isRange(rule) ? `${rule.listen_port}-${rule.listen_port_end}` : String(rule.listen_port || "");
}

/** parsePortText accepts 8080 or 8080-8099 and rejects anything else. */
function parsePortText(text: string): { port: number; end?: number } | null {
  const match = text.trim().match(/^(\d{1,5})(?:\s*-\s*(\d{1,5}))?$/);
  if (!match) return null;
  const port = Number(match[1]);
  if (port < 1 || port > 65535) return null;
  if (!match[2]) return { port };
  const end = Number(match[2]);
  if (end <= port || end > 65535 || end - port + 1 > 1024) return null;
  return { port, end };
}

/**
 * FlowLanes edits a port forward as the path a connection takes: ports live in
 * the entry card, and every port has its own line to a block in the exit card
 * where it is either forwarded to one service or handed to the client as a
 * proxy. Rows stay aligned so one port's entry and exit read as a single line.
 */
export function FlowLanes({ data, config, targets, words: w, onChange }: FlowLanesProps) {
  const rules = config.port_rules?.length ? config.port_rules : [newRule(8080)];
  const base = rules[0];
  // Ports are edited as text so a half-typed range does not jump around.
  const [portDraft, setPortDraft] = useState<Record<string, string>>({});
  const [advanced, setAdvanced] = useState(false);

  const write = (next: PortRule[]) => {
    const transports = Array.from(
      new Set(next.flatMap((rule) => rule.transports ?? [])),
    ) as ("tcp" | "udp")[];
    onChange({
      port_rules: next,
      transports: transports.length ? transports : ["tcp"],
      listen_host: next[0]?.listen_host || "0.0.0.0",
      listen_port: next[0]?.listen_port || 8080,
    });
  };
  const update = (index: number, patch: Partial<PortRule>) =>
    write(rules.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)));
  const shared = (patch: Partial<PortRule>) =>
    write(rules.map((rule) => ({ ...rule, ...patch })));

  // One egress applies to the whole tunnel, so it is summarised on the exit card.
  const egressHint = (() => {
    const spec = base.egress;
    if (!spec) return "";
    if (spec.mode === "proxy") return `${w.gateway}: ${spec.proxy_url || ""}`;
    if (spec.ipv6_mode === "pool") return `${w.ipv6Pool}: ${spec.pool_start || ""}`;
    if (spec.ipv6_mode === "interface") return `${w.pinnedNIC}: ${spec.interface || ""}`;
    return "";
  })();

  const picker = (side: "entry" | "exit") => (
    <MachinePicker
      targets={targets}
      folders={data.targetFolders}
      currentTargetID={side === "entry" ? config.entry_target_id : config.exit_target_id}
      label={side === "entry" ? w.entryMachine : w.exitMachine}
      variant="field"
      additionalOptions={data.user.is_system_admin ? [{ value: "", label: w.bastion }] : []}
      onOpenTarget={(id) =>
        onChange(side === "entry" ? { entry_target_id: id } : { exit_target_id: id })
      }
    />
  );

  return (
    <div className="flow-lanes">
      <div className="lane-head">
        <div className="lane-head-cell">
          <span className="lane-step">1</span>
          <strong>{w.entry}</strong>
          {picker("entry")}
          <div className="lane-transports">
            <span>{w.protocols}</span>
            <Checkbox.Group
              options={["tcp", "udp"].map((value) => ({ label: value.toUpperCase(), value }))}
              value={base.transports}
              onChange={(values) => shared({ transports: values as ("tcp" | "udp")[] })}
            />
          </div>
        </div>
        <div className="lane-head-arrow" aria-hidden="true" />
        <div className="lane-head-cell">
          <span className="lane-step">2</span>
          <strong>{w.exit}</strong>
          {picker("exit")}
          <div className="lane-advanced">
            <button type="button" onClick={() => setAdvanced((open) => !open)}>
              {advanced ? w.advancedEgressHide : w.advancedEgress}
            </button>
            {!advanced && egressHint && <span className="lane-advanced-hint">{egressHint}</span>}
          </div>
          {advanced && (
            <EgressEditor
              value={base.egress}
              exitTargetID={config.exit_target_id}
              onChange={(egress) => shared({ egress })}
            />
          )}
        </div>
      </div>

      {rules.map((rule, index) => {
        const single = (rule.protocol ?? "auto") === "tcp";
        const text = portDraft[rule.id] ?? portText(rule);
        return (
          <div className="lane-row" key={rule.id}>
            <div className="lane-port">
              <span className="lane-port-label">{w.listenLabel}</span>
              <Input
                className="lane-host"
                value={rule.listen_host || ""}
                placeholder="0.0.0.0"
                aria-label={w.listen}
                onChange={(e) => update(index, { listen_host: e.target.value })}
              />
              <Tooltip title={w.portFormTip}>
                <Input
                  className="lane-port-range"
                  value={text}
                  placeholder="8080 或 8080-8099"
                  aria-label={w.port}
                  onChange={(e) => {
                    const raw = e.target.value;
                    setPortDraft((d) => ({ ...d, [rule.id]: raw }));
                    const parsed = parsePortText(raw);
                    if (parsed) {
                      update(index, {
                        listen_port: parsed.port,
                        listen_port_end: parsed.end,
                      });
                    }
                  }}
                  onBlur={() =>
                    setPortDraft((d) => {
                      const next = { ...d };
                      delete next[rule.id];
                      return next;
                    })
                  }
                />
              </Tooltip>
              {rules.length > 1 && (
                <Button
                  type="text"
                  danger
                  aria-label={w.removeRule}
                  icon={<Trash2 size={14} />}
                  onClick={() => write(rules.filter((_, i) => i !== index))}
                />
              )}
            </div>

            <div className="lane-link" aria-hidden="true">
              <ArrowRight size={16} />
            </div>

            <Card size="small" className="lane-card">
              <div className="lane-card-head">
                <Segmented
                  value={single ? "single" : "proxy"}
                  items={[
                    ["single", w.laneFixed],
                    ["proxy", w.laneClient],
                  ]}
                  onChange={(value) =>
                    update(index, {
                      protocol:
                        value === "single"
                          ? "tcp"
                          : rule.protocol === "tcp"
                            ? undefined
                            : rule.protocol,
                    })
                  }
                />
              </div>
              {single ? (
                <div className="lane-target">
                  <span className="lane-target-label">{w.targetLabel}</span>
                  <Input
                    className="lane-target-host"
                    value={rule.destination_host || "127.0.0.1"}
                    placeholder="127.0.0.1"
                    aria-label={w.targetHost}
                    onChange={(e) => update(index, { destination_host: e.target.value })}
                  />
                  <span className="lane-target-colon">:</span>
                  <InputNumber
                    className="lane-target-port"
                    min={1}
                    max={65535}
                    value={rule.destination_port || null}
                    aria-label={w.targetPort}
                    onChange={(v) => update(index, { destination_port: Number(v) || 1 })}
                  />
                </div>
              ) : (
                <p className="lane-card-hint">{w.laneClientHint}</p>
              )}
            </Card>
          </div>
        );
      })}

      <div className="lane-add">
        <Button
          type="dashed"
          icon={<Plus size={15} />}
          disabled={rules.length >= 64}
          onClick={() =>
            write([
              ...rules,
              {
                ...newRule(Math.min(65535, (base.listen_port || 8080) + 1)),
                transports: base.transports,
                protocol: base.protocol,
                destination_host: base.destination_host || "127.0.0.1",
                destination_port: base.destination_port || 80,
              },
            ])
          }
        >
          {w.addPort}
        </Button>
      </div>
    </div>
  );
}
