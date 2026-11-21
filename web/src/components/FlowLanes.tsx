import { Button, Card, Checkbox, Input, InputNumber, Select, Tooltip } from "antd";
import { HelpCircle } from "lucide-react";
import { ArrowRight, Plus, Trash2 } from "lucide-react";
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

function newRule(port: number): PortRule {
  return {
    id: `rule-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
    listen_host: "0.0.0.0",
    listen_port: port,
    transports: ["tcp"],
    destination_host: "127.0.0.1",
    destination_port: 80,
    protocol: "tcp",
  };
}

function isRange(rule: PortRule): boolean {
  return Boolean(rule.listen_port_end && rule.listen_port_end > rule.listen_port);
}

/**
 * FlowLanes edits a port forward as the path a connection takes: ports live in
 * the entry card, and every port has its own line to a block in the exit card
 * where it is either sent to one fixed service or handled as a proxy egress.
 * Rows stay aligned, so one port's entry and exit read as a single line.
 */
export function FlowLanes({ data, config, targets, words: w, onChange }: FlowLanesProps) {
  const rules = config.port_rules?.length ? config.port_rules : [newRule(8080)];
  const base = rules[0];

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
        </div>
      </div>

      {rules.map((rule, index) => {
        const single = (rule.protocol ?? "auto") === "tcp";
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
              <InputNumber
                className="lane-port-number"
                min={1}
                max={65535}
                value={rule.listen_port || null}
                aria-label={w.port}
                onChange={(v) => update(index, { listen_port: Number(v) || 0 })}
              />
              <Tooltip title={w.portFormTip}>
                <HelpCircle size={13} className="lane-tip" aria-label={w.portFormTip} />
              </Tooltip>
              <Select
                className="lane-form"
                value={isRange(rule) ? "range" : "single"}
                options={[
                  { value: "single", label: w.portFormSingle },
                  { value: "range", label: w.portFormRange },
                ]}
                onChange={(value) =>
                  update(index, {
                    listen_port_end:
                      value === "range"
                        ? Math.min(65535, (rule.listen_port || 8080) + 9)
                        : undefined,
                  })
                }
              />
              {isRange(rule) && (
                <InputNumber
                  className="lane-port-number"
                  min={1}
                  max={65535}
                  value={rule.listen_port_end || null}
                  aria-label={w.portEnd}
                  onChange={(v) =>
                    update(index, {
                      listen_port_end:
                        v && Number(v) > rule.listen_port ? Number(v) : undefined,
                    })
                  }
                />
              )}
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
                            ? "auto"
                            : rule.protocol,
                    })
                  }
                />
                {!single && (
                  <Select
                    className="lane-protocol"
                    value={rule.protocol ?? "auto"}
                    aria-label={w.ruleProtocol}
                    options={[
                      { value: "auto", label: w.ruleProtocolAuto },
                      { value: "http", label: w.ruleProtocolHttp },
                      { value: "socks", label: w.ruleProtocolSocks },
                    ]}
                    onChange={(value) =>
                      update(index, { protocol: value === "auto" ? undefined : value })
                    }
                  />
                )}
              </div>
              <p className="lane-card-hint">
                {single ? w.laneFixedHint : w.laneClientHint}
              </p>
              {single ? (
                <div className="lane-target">
                  <span className="lane-target-label">{w.targetLabel}</span>
                  <Input
                    value={rule.destination_host}
                    aria-label={w.targetHost}
                    placeholder={w.targetHost}
                    onChange={(e) => update(index, { destination_host: e.target.value })}
                  />
                  <InputNumber
                    min={1}
                    max={65535}
                    value={rule.destination_port || null}
                    aria-label={w.targetPort}
                    onChange={(v) => update(index, { destination_port: Number(v) || 1 })}
                  />
                </div>
              ) : (
                <EgressEditor
                  value={rule.egress}
                  exitTargetID={config.exit_target_id}
                  onChange={(egress) => update(index, { egress })}
                />
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
