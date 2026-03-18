Name:           rhelmon
Version:        0.1.0
Release:        1%{?dist}
Summary:        Lightweight real-time system monitoring agent for RHEL and SUSE
License:        MIT
URL:            https://github.com/amit25sep/rhelmon
Source0:        %{name}-%{version}.tar.gz
BuildArch:      x86_64

%description
rhelmon is a zero-dependency, single-binary system monitoring agent for
RHEL 9 and compatible distributions (Rocky Linux, AlmaLinux, openSUSE, SLES).

Features:
  - Real-time CPU, memory, disk, network metrics from /proc and /sys
  - Netdata-style live dashboard over WebSocket
  - 1 hour of in-memory metric history
  - Threshold-based alert engine with Slack and email notifications
  - Prometheus remote write and InfluxDB line protocol export
  - External plugin loader
  - Self-monitoring with /metrics endpoint

%prep
%setup -q

%build
# Pre-compiled static binary — nothing to build.

%install
rm -rf %{buildroot}

%ifarch x86_64
  BINARY=bin/rhelmon-linux-amd64
%endif
%ifarch aarch64
  BINARY=bin/rhelmon-linux-arm64
%endif

install -D -m 0755 ${BINARY}                  %{buildroot}/usr/bin/rhelmon
install -D -m 0755 configs/rhelmon-start.sh   %{buildroot}/usr/bin/rhelmon-start
install -D -m 0644 configs/rhelmon.service    %{buildroot}/usr/lib/systemd/system/rhelmon.service
install -D -m 0640 configs/rhelmon.conf       %{buildroot}/etc/rhelmon/rhelmon.conf
install -D -m 0644 configs/rhelmon-logrotate  %{buildroot}/etc/logrotate.d/rhelmon

install -D -m 0755 plugins/nginx_plugin.sh            %{buildroot}/etc/rhelmon/plugins.examples/nginx_plugin.sh
install -D -m 0755 plugins/redis_plugin.sh            %{buildroot}/etc/rhelmon/plugins.examples/redis_plugin.sh
install -D -m 0755 plugins/postgres_plugin.py         %{buildroot}/etc/rhelmon/plugins.examples/postgres_plugin.py
install -D -m 0755 plugins/systemd_services_plugin.sh %{buildroot}/etc/rhelmon/plugins.examples/systemd_services_plugin.sh

install -d -m 0755 %{buildroot}/etc/rhelmon/plugins
install -d -m 0750 %{buildroot}/var/log/rhelmon

%pre
getent group  rhelmon >/dev/null || groupadd -r rhelmon
getent passwd rhelmon >/dev/null || \
  useradd -r -g rhelmon -d /var/lib/rhelmon -s /sbin/nologin \
  -c "rhelmon monitoring agent" rhelmon
exit 0

%post
if command -v systemctl &>/dev/null; then
  systemctl daemon-reload 2>/dev/null || true
fi
echo ""
echo "  rhelmon %{version} installed."
echo "  Start:     systemctl enable --now rhelmon"
echo "  Dashboard: http://$(hostname -I 2>/dev/null | awk '{print $1}' || echo 'your-server-ip'):9000"
echo ""

%preun
if [ $1 -eq 0 ]; then
  if command -v systemctl &>/dev/null; then
    systemctl stop    rhelmon 2>/dev/null || true
    systemctl disable rhelmon 2>/dev/null || true
  fi
fi

%postun
if command -v systemctl &>/dev/null; then
  systemctl daemon-reload 2>/dev/null || true
fi

%files
%defattr(-,root,root,-)
%license LICENSE
%doc README.md

/usr/bin/rhelmon
/usr/bin/rhelmon-start
/usr/lib/systemd/system/rhelmon.service

%config(noreplace) %attr(0640,root,rhelmon) /etc/rhelmon/rhelmon.conf
/etc/logrotate.d/rhelmon

%dir %attr(0755,root,root)       /etc/rhelmon
%dir %attr(0755,rhelmon,rhelmon) /etc/rhelmon/plugins
%dir %attr(0755,root,root)       /etc/rhelmon/plugins.examples
/etc/rhelmon/plugins.examples/

%dir %attr(0750,rhelmon,rhelmon) /var/log/rhelmon

%changelog
* Wed Mar 18 2026 amit25sep <amit25sep@users.noreply.github.com> - 0.1.0-1
- Initial release — all hardcoded paths, no rpm macros
