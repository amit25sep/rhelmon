#!/bin/bash
# nginx_plugin.sh — reports NGINX metrics via stub_status
# Requires: nginx with ngx_http_stub_status_module enabled
#
# Add to nginx.conf:
#   location /nginx_status {
#     stub_status;
#     allow 127.0.0.1;
#     deny all;
#   }
#
# Install: cp nginx_plugin.sh /etc/rhelmon/plugins/ && chmod +x /etc/rhelmon/plugins/nginx_plugin.sh

NGINX_STATUS_URL="${NGINX_STATUS_URL:-http://127.0.0.1/nginx_status}"

# Fetch stub_status page
status=$(curl -sf --max-time 5 "$NGINX_STATUS_URL" 2>/dev/null)
if [ $? -ne 0 ] || [ -z "$status" ]; then
  echo "# nginx_plugin: could not reach $NGINX_STATUS_URL" >&2
  exit 1
fi

# Parse:
# Active connections: 42
# server accepts handled requests
#  1234 1234 5678
# Reading: 1 Writing: 2 Waiting: 39
active=$(echo "$status"    | grep 'Active connections' | awk '{print $3}')
reading=$(echo "$status"   | grep 'Reading'            | awk '{print $2}')
writing=$(echo "$status"   | grep 'Writing'            | awk '{print $4}')
waiting=$(echo "$status"   | grep 'Waiting'            | awk '{print $6}')
accepts=$(echo "$status"   | awk 'NR==3{print $1}')
handled=$(echo "$status"   | awk 'NR==3{print $2}')
requests=$(echo "$status"  | awk 'NR==3{print $3}')

# Emit key value pairs — rhelmon pushes these as plugin.nginx_plugin.<key>
echo "active_connections ${active:-0}"
echo "reading ${reading:-0}"
echo "writing ${writing:-0}"
echo "waiting ${waiting:-0}"
echo "accepts ${accepts:-0}"
echo "handled ${handled:-0}"
echo "requests ${requests:-0}"
