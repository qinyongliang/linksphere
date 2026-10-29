# Android IPv6 router refresh

When periodic IPv6 router advertisements stop reaching an Android device, its
learned IPv6 default route expires. Android can treat
that event as loss of IP provisioning and disconnect Wi-Fi, even while its IPv4
address, gateway and DHCP lease remain usable. A failed background network scan
can also delay reconnection.

For devices where diagnostics confirm this behavior, the native Android Agent
accepts `--android-ipv6-router-refresh-interface wlan0`. This option is disabled
by default and requires an existing root execution context and raw socket access.
Other platforms reject the option. The APK configuration does not enable it.

The worker checks existing IPv6 routes every 30 seconds using `/system/bin/ip`.
When an RA default route on the selected interface has at most 120 seconds left,
it sends one standard ICMPv6 Router Solicitation from that interface's link-local
address, with hop limit 255. Attempts are limited to once per minute. The router
decides whether to answer and which configuration to advertise.

This preserves the device's network settings and existing route ownership. The
worker stops with the Agent, including during an update. Check the Agent log and
route lifetime after enabling it, then verify that the screen can remain off
beyond the previously observed failure interval. Remove the option to disable
the workaround.
