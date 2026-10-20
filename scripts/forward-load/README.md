# Isolated forwarding validation

This directory contains a reproducible Linux network-namespace lab for local HTTP and TCP/UDP forwarding checks.

Requirements: Linux with `iproute2`, Python 3, a matching Go SDK, and permission to create the lab's temporary network namespaces. Race checks also require a C compiler. The WSL wrappers run the same lab from a Windows development machine.

- `network.sh`: creates the isolated topology and runs validation.
- `runner.py`: coordinates synthetic backends, services and traffic.
- `run-wsl.sh` / `test-wsl.sh`: run the lab or its checks through WSL.
- `race-wsl.sh`: enables the race detector.
- `setup-wsl-go.sh`: downloads and verifies the Go SDK into a user-owned cache.

The benchmark uses the reserved `198.18.0.0/15` address range inside its isolated namespaces. It must not target production services. Generated databases, logs, screenshots and results are local-only and must remain outside version control.
