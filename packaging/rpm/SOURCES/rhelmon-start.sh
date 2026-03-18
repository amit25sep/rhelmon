#!/bin/bash
# /usr/bin/rhelmon-start
# Sources /etc/rhelmon/rhelmon.conf and launches rhelmon with real values.
# Do NOT edit directly — edit /etc/rhelmon/rhelmon.conf then restart the service.

CONFIG=/etc/rhelmon/rhelmon.conf
[ -f "$CONFIG" ] && source "$CONFIG"

exec /usr/bin/rhelmon \
  -addr            "${ADDR:-:9000}" \
  -history         "${HISTORY:-3600}" \
  -interval        "${INTERVAL:-1s}" \
  -broadcast       "${BROADCAST:-1s}" \
  -plugin-dir      "${PLUGIN_DIR:-/etc/rhelmon/plugins}" \
  -plugin-interval "${PLUGIN_INTERVAL:-30s}" \
  -plugin-timeout  "${PLUGIN_TIMEOUT:-10s}" \
  ${SLACK_WEBHOOK:+  -slack-webhook  "$SLACK_WEBHOOK"} \
  ${SLACK_CHANNEL:+  -slack-channel  "$SLACK_CHANNEL"} \
  ${SMTP_HOST:+      -smtp-host      "$SMTP_HOST"} \
  ${SMTP_PORT:+      -smtp-port      "$SMTP_PORT"} \
  ${SMTP_USER:+      -smtp-user      "$SMTP_USER"} \
  ${SMTP_PASS:+      -smtp-pass      "$SMTP_PASS"} \
  ${SMTP_FROM:+      -smtp-from      "$SMTP_FROM"} \
  ${SMTP_TO:+        -smtp-to        "$SMTP_TO"} \
  ${PROM_URL:+       -prom-url       "$PROM_URL"} \
  ${PROM_USER:+      -prom-user      "$PROM_USER"} \
  ${PROM_PASSWORD:+  -prom-password  "$PROM_PASSWORD"} \
  ${PROM_BEARER:+    -prom-bearer    "$PROM_BEARER"} \
  ${INFLUX_URL:+     -influx-url     "$INFLUX_URL"} \
  ${INFLUX_TOKEN:+   -influx-token   "$INFLUX_TOKEN"} \
  ${INFLUX_ORG:+     -influx-org     "$INFLUX_ORG"} \
  ${INFLUX_BUCKET:+  -influx-bucket  "$INFLUX_BUCKET"} \
  ${INFLUX_V1DB:+    -influx-v1db    "$INFLUX_V1DB"} \
  ${INFLUX_V1USER:+  -influx-v1user  "$INFLUX_V1USER"} \
  ${INFLUX_V1PASS:+  -influx-v1pass  "$INFLUX_V1PASS"}
