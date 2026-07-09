#!/usr/bin/env bash
# Generates a self-signed TLS cert for local dev/testing of browser features
# that require a secure context (camera, etc. — see web/static/index.html's
# QR scanner). Not for production use.
#
# Usage: scripts/gen-dev-cert.sh [lan-ip]
# If lan-ip is omitted, it's auto-detected. Re-run this whenever your LAN IP
# changes (e.g. switching WiFi networks).
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p certs

LAN_IP="${1:-}"
if [ -z "$LAN_IP" ]; then
  LAN_IP=$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || true)
fi
if [ -z "$LAN_IP" ]; then
  echo "Could not auto-detect a LAN IP. Pass it explicitly: $0 <lan-ip>" >&2
  exit 1
fi

echo "Generating self-signed cert for localhost, 127.0.0.1, and $LAN_IP ..."

CNF=$(mktemp)
trap 'rm -f "$CNF"' EXIT

cat > "$CNF" <<EOF
[req]
distinguished_name = req_distinguished_name
x509_extensions = v3_req
prompt = no

[req_distinguished_name]
CN = $LAN_IP

[v3_req]
keyUsage = keyEncipherment, dataEncipherment, digitalSignature
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = localhost
IP.1 = 127.0.0.1
IP.2 = $LAN_IP
EOF

openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
  -keyout certs/dev.key -out certs/dev.crt \
  -config "$CNF" -extensions v3_req

echo "Done: certs/dev.crt certs/dev.key (valid for localhost, 127.0.0.1, $LAN_IP)"
