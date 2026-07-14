#!/bin/bash

# --- CONFIGURATION ---
# Change this to the actual path of your database file
DB_FILE="/home/pi/foswvs-go/foswvs.db"

# Check if database file exists
if [ ! -f "$DB_FILE" ]; then
    echo "Error: Database file $DB_FILE not found."
    exit 1
fi

echo "Starting database cleanup (30 days)..."

# We use a single sqlite3 call to run multiple commands
sqlite3 "$DB_FILE" <<EOF
-- 1. Delete sessions that are older than 30 days
DELETE FROM session 
WHERE updated_at < datetime('now', '-30 days');

-- 2. Delete sessions belonging to devices that are older than 30 days 
-- (This prevents foreign key errors when we delete the devices)
DELETE FROM session 
WHERE device_id IN (
    SELECT id FROM devices WHERE updated_at < datetime('now', '-30 days')
);

-- 3. Delete devices that are older than 30 days
DELETE FROM devices 
WHERE updated_at < datetime('now', '-30 days');

-- 4. Optimize the database to reclaim unused space
VACUUM;
EOF

if [ $? -eq 0 ]; then
    echo "Cleanup completed successfully."
else
    echo "An error occurred during cleanup."
fi

