# Forwarding egress

Connection forwarding (port and HTTP) can choose **how the exit machine opens the
upstream socket**: the host routing table (default), a pinned network interface, a
pool of IPv6 source addresses, or an upstream SOCKS5/HTTP proxy. The option is set
per rule, so one configuration can listen on several ports with different egress
behaviour.

Nothing about the traffic path changes: the entry machine still listens, the exit
machine still dials, and both segments keep using the existing relay and P2P
channel. Only the final socket on the exit machine gains the source selection.

## Data model

The rule carries an optional egress selection:

    {
      "mode": "direct" | "proxy",
      "family": "auto" | "ipv4" | "ipv6" | "both",
      "interface": "eth0",
      "ipv6_mode": "host" | "interface" | "pool",
      "pool_start": "2001:db8:100::100",
      "pool_size": 64,
      "pool_mode": "rotate" | "sticky",
      "proxy_url": "socks5://proxy.example:1080"
    }

* "mode": "direct" (default) opens the socket from the exit host. "family" restricts
  resolution to IPv4, IPv6, both or lets the host decide ("auto").
* "interface" pins the socket to one NIC (Linux: SO_BINDTODEVICE). It is the answer
  for hosts with several uplinks.
* "ipv6_mode": "interface" uses the first global IPv6 address of that NIC as the
  source address; "host" leaves source selection to the kernel.
* "ipv6_mode": "pool" allocates a source address per connection from the contiguous
  range pool_start ... pool_start+pool_size-1. "pool_mode": "rotate" picks a random
  member every connection; "sticky" advances from the previous address by a random
  increment, which keeps a stable address per client while spreading clients over
  the prefix.
* "mode": "proxy" tunnels the TCP connection through an upstream SOCKS5 or HTTP
  CONNECT proxy reachable from the exit machine. Credentials may be embedded in the
  URL. UDP is not tunnelled.

The default ("direct", "auto", "host") is serialised as an absent "egress" field.

## Multiple listen ports

A port-forward configuration may carry "port_rules". Each rule owns its listen
address, transports, destination and egress selection. Adding "listen_port_end"
turns the rule into an inclusive port range (for example 10000-11000): every port
in the range gets its own listener but shares the rule's route grants, destination
and egress, so a pool still allocates one source address per connection.

    {
      "protocol": "port",
      "port_rules": [
        {"id": "web", "listen_host": "0.0.0.0", "listen_port": 8888,
         "transports": ["tcp"], "destination_host": "192.0.2.10", "destination_port": 80,
         "egress": {"family": "both"}},
        {"id": "rot", "listen_host": "0.0.0.0", "listen_port": 9999,
         "transports": ["tcp"], "destination_host": "192.0.2.10", "destination_port": 443,
         "egress": {"family": "ipv6", "ipv6_mode": "pool",
                    "pool_start": "2001:db8:100::100", "pool_size": 64,
                    "pool_mode": "rotate"}},
        {"id": "seq", "listen_host": "0.0.0.0", "listen_port": 10000,
         "transports": ["tcp"], "destination_host": "192.0.2.10", "destination_port": 443,
         "egress": {"family": "ipv6", "ipv6_mode": "pool",
                    "pool_start": "2001:db8:100::100", "pool_size": 64,
                    "pool_mode": "sticky"}}
      ]
    }

Configurations saved before multi-port support keep using the single-port fields;
the server projects them onto one rule with the reserved id "port".

HTTP forwarding applies egress per location:

    {"id": "loc", "path": "/api", "scheme": "http", "host": "192.0.2.10", "port": 8080,
     "exit_target_id": "target-id", "egress": {"interface": "eth1", "family": "ipv4"}}

## Runtime placement

| Egress capability | Where it runs | Requirement |
| --- | --- | --- |
| host default, family restriction | exit Agent or bastion | none |
| pinned interface | exit Agent or bastion | Linux (SO_BINDTODEVICE) |
| IPv6 pool | exit Agent or bastion | Linux, delegated prefix routed to the host |
| upstream proxy | exit Agent or bastion | proxy reachable from the exit |

SSH-only exit machines cannot be queried for interfaces and do not expose the
socket, so advanced egress is rejected at save time. Keep the default egress for
those, or use SSH dynamic forwarding on that machine.

Pool allocation is process-local and keyed by (machine, pool_start, pool_mode). The
exit Agent assigns each allocated address to a dedicated dummy device (lsv6pool)
so replies are delivered to the machine, then binds it as the source address.
Without root it falls back to IPV6_FREEBIND, which can send but cannot receive, so
a delegated prefix routed to the machine and a root Agent are the supported setup.

## Console and MCP

* GET /api/targets/{id}/net returns the exit machine interfaces, addresses and
  default source addresses, or supported:false when the endpoint is not an Agent.
* MCP: tunnel_create and tunnel_update accept port_rules (with per-rule egress) and
  tunnel_net_info lists a machine egress interfaces so an assistant can pick one.

## Validation summary

* every rule validates its egress with the same rules the exit machine applies;
* non-default egress requires an Agent exit machine;
* listen host/port pairs must be unique per configuration, including overlap
  between a range and any other rule on the same host;
* UDP needs a non-SSH exit;
* proxy egress cannot be combined with an interface or IPv6 pool selection.

## Limitations

* A port range is capped at 1024 ports per rule. Every port in the range binds its
  own listener but shares the rule route grants, destination and egress selection;
  the status line folds them back into "host:10000-11000".
* Interface binding and IPv6 pools require a Linux exit (SO_BINDTODEVICE and
  IPV6_FREEBIND). Other platforms accept the default host egress only.
* Pool addresses are assigned to the lsv6pool dummy device on the exit machine;
  the Agent never adds routes and never touches other interfaces. Removing the
  dummy device releases every allocated address.
* The sticky allocation cursor is per process; an Agent restart restarts the
  sequence at the pool base.
* Proxy egress is TCP-only and carries no UDP forwarding.
