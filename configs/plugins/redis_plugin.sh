#!/bin/bash
# redis_plugin.sh — reports Redis metrics via redis-cli INFO
#
# Configuration via environment variables:
#   REDIS_HOST    default: 127.0.0.1
#   REDIS_PORT    default: 6379
#   REDIS_PASS    default: (empty)
#
# Install: cp redis_plugin.sh /etc/rhelmon/plugins/ && chmod +x /etc/rhelmon/plugins/redis_plugin.sh

REDIS_HOST="${REDIS_HOST:-127.0.0.1}"
REDIS_PORT="${REDIS_PORT:-6379}"
REDIS_PASS="${REDIS_PASS:-}"

if ! command -v redis-cli &>/dev/null; then
  echo "# redis_plugin: redis-cli not found" >&2
  exit 1
fi

REDIS_CMD="redis-cli -h $REDIS_HOST -p $REDIS_PORT"
if [ -n "$REDIS_PASS" ]; then
  REDIS_CMD="$REDIS_CMD -a $REDIS_PASS"
fi

info=$($REDIS_CMD INFO all 2>/dev/null)
if [ $? -ne 0 ] || [ -z "$info" ]; then
  echo "# redis_plugin: could not connect to $REDIS_HOST:$REDIS_PORT" >&2
  exit 1
fi

get_field() {
  echo "$info" | grep "^$1:" | cut -d: -f2 | tr -d '\r'
}

connected_clients=$(get_field connected_clients)
blocked_clients=$(get_field blocked_clients)
used_memory=$(get_field used_memory)
used_memory_rss=$(get_field used_memory_rss)
mem_fragmentation_ratio=$(get_field mem_fragmentation_ratio)
total_commands=$(get_field total_commands_processed)
total_connections=$(get_field total_connections_received)
instantaneous_ops=$(get_field instantaneous_ops_per_sec)
keyspace_hits=$(get_field keyspace_hits)
keyspace_misses=$(get_field keyspace_misses)
expired_keys=$(get_field expired_keys)
evicted_keys=$(get_field evicted_keys)
rdb_last_bgsave_status=$([ "$(get_field rdb_last_bgsave_status)" = "ok" ] && echo 1 || echo 0)

# Convert bytes to MB
used_memory_mb=$(echo "scale=2; ${used_memory:-0} / 1048576" | bc 2>/dev/null || echo 0)
used_memory_rss_mb=$(echo "scale=2; ${used_memory_rss:-0} / 1048576" | bc 2>/dev/null || echo 0)

echo "connected_clients ${connected_clients:-0}"
echo "blocked_clients ${blocked_clients:-0}"
echo "used_memory_mb ${used_memory_mb:-0}"
echo "used_memory_rss_mb ${used_memory_rss_mb:-0}"
echo "mem_fragmentation_ratio ${mem_fragmentation_ratio:-0}"
echo "total_commands ${total_commands:-0}"
echo "total_connections ${total_connections:-0}"
echo "instantaneous_ops_per_sec ${instantaneous_ops:-0}"
echo "keyspace_hits ${keyspace_hits:-0}"
echo "keyspace_misses ${keyspace_misses:-0}"
echo "expired_keys ${expired_keys:-0}"
echo "evicted_keys ${evicted_keys:-0}"
echo "rdb_last_bgsave_ok ${rdb_last_bgsave_status:-0}"
