#!/usr/bin/env python3
# postgres_plugin.py — reports PostgreSQL connection and activity metrics
# Requires: psycopg2 (pip install psycopg2-binary) or pg8000 (pip install pg8000)
#
# Configuration via environment variables:
#   PG_HOST     default: localhost
#   PG_PORT     default: 5432
#   PG_USER     default: postgres
#   PG_PASS     default: (empty)
#   PG_DB       default: postgres
#
# Install: cp postgres_plugin.py /etc/rhelmon/plugins/ && chmod +x /etc/rhelmon/plugins/postgres_plugin.py

import os
import sys

host = os.environ.get("PG_HOST", "localhost")
port = int(os.environ.get("PG_PORT", "5432"))
user = os.environ.get("PG_USER", "postgres")
password = os.environ.get("PG_PASS", "")
dbname = os.environ.get("PG_DB", "postgres")

try:
    import psycopg2
    conn = psycopg2.connect(host=host, port=port, user=user, password=password, dbname=dbname, connect_timeout=5)
except ImportError:
    try:
        import pg8000.native as pg8000
        conn = pg8000.Connection(user=user, password=password, host=host, port=port, database=dbname)
        # pg8000 uses different API — wrap it
        class Pg8000Wrapper:
            def __init__(self, c):
                self._c = c
            def cursor(self):
                return self
            def execute(self, q):
                self._result = self._c.run(q)
            def fetchone(self):
                return self._result[0] if self._result else None
            def fetchall(self):
                return self._result
            def close(self):
                pass
        conn = Pg8000Wrapper(conn)
    except ImportError:
        print("# postgres_plugin: neither psycopg2 nor pg8000 is installed", file=sys.stderr)
        print("# install with: pip3 install psycopg2-binary", file=sys.stderr)
        sys.exit(1)
except Exception as e:
    print(f"# postgres_plugin: connection failed: {e}", file=sys.stderr)
    sys.exit(1)

try:
    cur = conn.cursor()

    # Total connections
    cur.execute("SELECT count(*) FROM pg_stat_activity")
    row = cur.fetchone()
    print(f"total_connections {row[0] if row else 0}")

    # Active (not idle) connections
    cur.execute("SELECT count(*) FROM pg_stat_activity WHERE state != 'idle'")
    row = cur.fetchone()
    print(f"active_connections {row[0] if row else 0}")

    # Idle connections
    cur.execute("SELECT count(*) FROM pg_stat_activity WHERE state = 'idle'")
    row = cur.fetchone()
    print(f"idle_connections {row[0] if row else 0}")

    # Waiting connections (lock wait)
    cur.execute("SELECT count(*) FROM pg_stat_activity WHERE wait_event_type IS NOT NULL")
    row = cur.fetchone()
    print(f"waiting_connections {row[0] if row else 0}")

    # Transactions per second (cumulative — delta handled by rhelmon ring buffer)
    cur.execute("SELECT sum(xact_commit) + sum(xact_rollback) FROM pg_stat_database")
    row = cur.fetchone()
    print(f"total_transactions {row[0] if row else 0}")

    # Cache hit ratio (across all databases)
    cur.execute("""
        SELECT round(
            sum(blks_hit)::numeric /
            nullif(sum(blks_hit) + sum(blks_read), 0) * 100, 2
        ) FROM pg_stat_database
    """)
    row = cur.fetchone()
    print(f"cache_hit_pct {row[0] if row and row[0] is not None else 0}")

    # Deadlocks (cumulative)
    cur.execute("SELECT sum(deadlocks) FROM pg_stat_database")
    row = cur.fetchone()
    print(f"deadlocks {row[0] if row and row[0] is not None else 0}")

    cur.close()
    conn.close() if hasattr(conn, 'close') else None

except Exception as e:
    print(f"# postgres_plugin: query failed: {e}", file=sys.stderr)
    sys.exit(1)
