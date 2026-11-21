import { Button, Checkbox, Input, InputNumber, Select } from "antd";
import { ArrowDown, Plus, Trash2 } from "lucide-react";
import type { PortRule, TunnelConfig } from "../types";
import { EgressEditor } from "./EgressEditor";
import { Segmented } from "./ui";
import "./port-layers.css";

type Words = Record<string, string>;

export type TunnelMode = "single" | "proxy";

/** Where the client is told to send traffic. */
export interface LayerProps {
  config: TunnelConfig;
  words: Words;
  mode: TunnelMode;
  onChange: (patch: Partial<TunnelConfig>) => void;
}

function defaultRule(): PortRule {
  return {
    id: `rule-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
    listen_host: "0.0.0.0",
    listen_port: 8080,
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
 * PortRuleLayers presents one tunnel as the path a connection takes: the ports
 * it can arrive on, how it is carried, where it leaves from and — for a single
 * destination — the fixed target. Every layer is edited in place, so the data
 * flow is visible instead of hidden behind a flat list of fields.
 */
export function PortRuleLayers({ config, words: w, mode, onChange }: LayerProps) {
  const rules = config.port_rules?.length ? config.port_rules : [defaultRule()];
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
  /** shared applies one change to every port: transport, protocol and egress. */
  const shared = (patch: Partial<PortRule>) =>
    write(rules.map((rule) => ({ ...rule, ...patch })));
  const update = (index: number, patch: Partial<PortRule>) =>
    write(rules.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)));

  const setMode = (next: TunnelMode) => {
    if (next === "single") {
      write([
        {
          ...base,
          protocol: "tcp",
          listen_port_end: undefined,
          transports: ["tcp"],
        },
      ]);
      return;
    }
    write(rules.map((rule) => ({ ...rule, protocol: rule.protocol === "tcp" ? "auto" : rule.protocol })));
  };

  return (
    <div className="port-layers">
      <div className="layer-mode">
        <span>{w.modeLabel}</span>
        <Segmented
          value={mode}
          items={[
            ["single", w.modeSingle],
            ["proxy", w.modeProxy],
          ]}
          onChange={(value) => setMode(value as TunnelMode)}
        />
      </div>
      <span className="tunnel-help">
        {mode === "single" ? w.modeSingleHelp : w.modeProxyHelp}
      </span>

      <section className="tunnel-layer">
        <header>
          <b>1</b>
          <strong>{w.layerListen}</strong>
          <span className="tunnel-help">{w.layerListenHelp}</span>
        </header>
        <div className="layer-body">
          {rules.map((rule, index) => (
            <div className="port-row" key={rule.id}>
              <label className="field">
                <span>{w.listen}</span>
                <Input
                  value={rule.listen_host || ""}
                  placeholder="0.0.0.0"
                  onChange={(e) => update(index, { listen_host: e.target.value })}
                />
              </label>
              <label className="field">
                <span>{w.portForm}</span>
                <Select
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
              </label>
              <label className="field">
                <span>{w.port}</span>
                <InputNumber
                  min={1}
                  max={65535}
                  value={rule.listen_port || null}
                  onChange={(v) => update(index, { listen_port: Number(v) || 0 })}
                />
              </label>
              {isRange(rule) && (
                <label className="field">
                  <span>{w.portEnd}</span>
                  <InputNumber
                    min={1}
                    max={65535}
                    value={rule.listen_port_end || null}
                    onChange={(v) =>
                      update(index, {
                        listen_port_end:
                          v && Number(v) > rule.listen_port ? Number(v) : undefined,
                      })
                    }
                  />
                </label>
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
          ))}
          {mode === "proxy" && (
            <Button
              type="dashed"
              size="small"
              icon={<Plus size={14} />}
              disabled={rules.length >= 64}
              onClick={() =>
                write([
                  ...rules,
                  {
                    ...base,
                    id: defaultRule().id,
                    listen_port: Math.min(65535, (base.listen_port || 8080) + 1),
                    listen_port_end: undefined,
                  },
                ])
              }
            >
              {w.addPort}
            </Button>
          )}
        </div>
      </section>

      <div className="layer-arrow">
        <ArrowDown size={16} />
      </div>

      <section className="tunnel-layer">
        <header>
          <b>2</b>
          <strong>{w.layerTransport}</strong>
          <span className="tunnel-help">{w.layerTransportHelp}</span>
        </header>
        <div className="layer-body">
          <div className="field">
            <span>{w.protocols}</span>
            <Checkbox.Group
              options={["tcp", "udp"].map((value) => ({ label: value.toUpperCase(), value }))}
              value={base.transports}
              onChange={(values) => shared({ transports: values as ("tcp" | "udp")[] })}
            />
          </div>
          {mode === "proxy" && (
            <label className="field">
              <span>{w.ruleProtocol}</span>
              <Select
                value={base.protocol ?? "auto"}
                options={[
                  { value: "auto", label: w.ruleProtocolAuto },
                  { value: "http", label: w.ruleProtocolHttp },
                  { value: "socks", label: w.ruleProtocolSocks },
                  { value: "tcp", label: w.ruleProtocolTcp },
                ]}
                onChange={(value) => shared({ protocol: value === "auto" ? undefined : value })}
              />
            </label>
          )}
        </div>
      </section>

      <div className="layer-arrow">
        <ArrowDown size={16} />
      </div>

      <section className="tunnel-layer">
        <header>
          <b>3</b>
          <strong>{w.layerEgress}</strong>
          <span className="tunnel-help">{w.layerEgressHelp}</span>
        </header>
        <div className="layer-body">
          <EgressEditor
            value={base.egress}
            exitTargetID={config.exit_target_id}
            onChange={(egress) => shared({ egress })}
          />
        </div>
      </section>

      {mode === "single" ? (
        <>
          <div className="layer-arrow">
            <ArrowDown size={16} />
          </div>
          <section className="tunnel-layer">
            <header>
              <b>4</b>
              <strong>{w.layerDestination}</strong>
              <span className="tunnel-help">{w.layerDestinationHelp}</span>
            </header>
            <div className="layer-body">
              <label className="field">
                <span>{w.targetHost}</span>
                <Input
                  value={base.destination_host}
                  onChange={(e) => shared({ destination_host: e.target.value })}
                />
              </label>
              <label className="field">
                <span>{w.targetPort}</span>
                <InputNumber
                  min={1}
                  max={65535}
                  value={base.destination_port || null}
                  onChange={(v) => shared({ destination_port: Number(v) || 1 })}
                />
              </label>
            </div>
          </section>
        </>
      ) : (
        <p className="tunnel-help layer-note">{w.layerDestinationProxy}</p>
      )}
    </div>
  );
}
