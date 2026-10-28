#!/bin/sh
# Incremental (physical) backups use the replication protocol: allow it like normal connections.
echo "host replication all all scram-sha-256" >> "$PGDATA/pg_hba.conf"
