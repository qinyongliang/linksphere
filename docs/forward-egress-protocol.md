## One port, several proxy protocols

A port rule may declare "protocol". Absent or "auto" makes the listener sniff the
first bytes and serve whichever proxy protocol the client speaks:

| Client | Detected by | Behaviour |
| --- | --- | --- |
| SOCKS5 | first byte 0x05 | RFC 1928 CONNECT; BIND and UDP ASSOCIATE are refused |
| SOCKS4 | first byte 0x04 | SOCKS4 / SOCKS4a CONNECT |
| HTTP proxy | an ASCII method | CONNECT (HTTPS tunnels) and absolute-form requests |
| anything else | any other leading byte | raw stream to the rule destination |

"tcp" pins the listener to the raw stream, "http" and "socks" pin one proxy
protocol and drop foreign clients. Every proxied connection still leaves through
the same route grant, so the rule egress (interface, IPv6 pool, upstream proxy)
applies per connection.

    "port_rules": [
      {"id": "mixed", "listen_host": "0.0.0.0", "listen_port": 1080,
       "transports": ["tcp"], "destination_host": "127.0.0.1", "destination_port": 80,
       "protocol": "auto",
       "egress": {"family": "ipv6", "ipv6_mode": "pool",
                  "pool_start": "2001:db8:100::100", "pool_size": 64,
                  "pool_mode": "rotate"}}
    ]

The same rule answers "curl -x http://host:1080 https://example.com" and
"curl -x socks5h://host:1080 https://example.com".

Because a proxy client picks the destination, the TCP route grant is marked
dynamic: the entry machine sends the requested address with each connection and
the exit machine refuses a destination on a non-dynamic grant. UDP is never
dynamic; it always streams to the rule destination.
