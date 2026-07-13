#!/usr/bin/env bash
set -euo pipefail

DEST_DB="/home/pi/foswvs-go/foswvs.db"
SRC_DB="/home/pi/foswvs/conf/foswvs.db"

sqlite3 "$DEST_DB" <<EOF
ATTACH DATABASE '$SRC_DB' AS src;

INSERT OR IGNORE INTO devices (mac_addr,ip_addr,hostname,topup_count,created_at,updated_at,topup_at)
SELECT mac_addr,ip_addr,hostname,topup_count,created_at,updated_at,topup_at FROM src.devices;

INSERT OR IGNORE INTO session
SELECT * FROM src.session;

DETACH DATABASE src;
EOF

echo "Database copy completed."
