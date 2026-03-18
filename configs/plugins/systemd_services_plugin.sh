#!/bin/bash
# systemd_services_plugin.sh — checks if critical systemd services are active
#
# Configuration:
#   RHELMON_SERVICES  space-separated list of service names to check
#                     default: "sshd crond firewalld"
#
# Emits:  <service_name>_active 1   (if active/running)
#         <service_name>_active 0   (if inactive/failed/missing)
#
# Install: cp systemd_services_plugin.sh /etc/rhelmon/plugins/ && chmod +x ...

SERVICES="${RHELMON_SERVICES:-sshd crond firewalld}"

for svc in $SERVICES; do
  # strip .service suffix if provided
  name="${svc%.service}"
  # sanitise name for metric key: replace - with _
  key="${name//-/_}_active"

  state=$(systemctl is-active "${name}.service" 2>/dev/null)
  if [ "$state" = "active" ]; then
    echo "$key 1"
  else
    echo "$key 0"
  fi
done
