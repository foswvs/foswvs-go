#!/usr/bin/env bash
# Base iptables rules for the PisoWiFi captive portal.
#
# The Go binary only ever adds/removes per-client ACCEPT rules as devices
# pay for data (see internal/iptables.AddClient/RemoveClient) — it never
# sets up the underlying NAT/redirect/forward policy itself
# (internal/iptables.RestoreBaseRules exists but nothing calls it). That's
# the OS's job, and this script is it. Run once at boot, before foswvs-go
# starts — see foswvs-go.service's ExecStartPre.
#
# Idempotent: safe to re-run (e.g. on service restart) without piling up
# duplicate rules.
set -euo pipefail

PORTAL_IP="${PORTAL_IP:-10.0.0.1}"

# Belt-and-suspenders: /etc/sysctl.d should already persist this (see
# INSTALL.md), but set it live too so a skipped sysctl step doesn't
# silently break forwarding.
sysctl -w net.ipv4.ip_forward=1 >/dev/null

# Two variants (rather than one function juggling an optional -t table arg)
# to sidestep empty-array expansion under `set -u`, which older bash (e.g.
# macOS's stock 3.2, and some minimal Debian images) mishandles.
add_filter_if_missing() {
  if ! iptables -C "$@" 2>/dev/null; then
    iptables -A "$@"
  fi
}
add_nat_if_missing() {
  if ! iptables -t nat -C "$@" 2>/dev/null; then
    iptables -t nat -A "$@"
  fi
}

# Default-deny forwarding; foswvs-go opens per-client ACCEPT rules as
# devices pay for data.
iptables -P FORWARD DROP

# Block portal clients from reaching other private ranges directly.
add_filter_if_missing FORWARD -p tcp -d 192.168.0.0/16 -j REJECT

# Force all DNS through this box (captive portal DNS interception).
add_nat_if_missing PREROUTING -p udp --dport 53 -j REDIRECT

# Redirect unauthenticated HTTP/HTTPS to the portal itself.
add_nat_if_missing PREROUTING -p tcp -m multiport --dports 80,443 -j DNAT --to-destination "$PORTAL_IP"

# NAT paid clients out to the internet via the Pi's uplink.
add_nat_if_missing POSTROUTING -j MASQUERADE

echo "iptables-base: rules applied (portal IP $PORTAL_IP)"
