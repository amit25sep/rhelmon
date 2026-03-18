Name:           rhelmon
Version:        0.1.0
Release:        1%{?dist}
Summary:        Lightweight real-time system monitoring agent for RHEL and SUSE
License:        MIT
URL:            https://github.com/amit25sep/rhelmon

# Source tarball layout expected:
#   rhelmon-0.1.0/
#     bin/rhelmon-linux-amd64
#     bin/rhelmon-linux-arm64
#     configs/rhelmon.conf
#     configs/rhelmon-start.sh
#     configs/rhelmon.service
#     configs/rhelmon-logrotate
#     plugins/nginx_plugin.sh
#     plugins/redis_plugin.sh
#     plugins/postgres_plugin.py
#     plugins/systemd_services_plugin.sh
Source0:        %{name}-%{version}.tar.gz

BuildArch:      x86_64
BuildRequires:  systemd-rpm-macros

Requires:       systemd
Requires(pre):  shadow-utils
Requires(post): systemd
Requires(preun): systemd
Requires(postun): systemd

%description
rhelmon is a zero-dependency, single-binary system monitoring agent for
RHEL 9 and compatible distributions (Rocky Linux, AlmaLinux, openSUSE, SLES).

Features:
  - Real-time CPU, memory, disk, network metrics from /proc and /sys
  - Netdata-style live dashboard over WebSocket (no browser plugin needed)
  - 1 hour of in-memory metric history (ring buffer, 3600 samples/metric)
  - Threshold-based alert engine with Slack and email notifications
  - Prometheus remote write and InfluxDB line protocol export
  - External plugin loader (drop any executable in /etc/rhelmon/plugins/)
  - Self-monitoring: goroutine count, heap, GC, /metrics endpoint

%prep
%setup -q

%build
# Pre-compiled static binary — nothing to build.

%install
rm -rf %{buildroot}

# Detect architecture
%ifarch x86_64
  BINARY=bin/rhelmon-linux-amd64
%endif
%ifarch aarch64
  BINARY=bin/rhelmon-linux-arm64
%endif

install -D -m 0755 ${BINARY}                  %{buildroot}%{_bindir}/rhelmon
install -D -m 0755 configs/rhelmon-start.sh   %{buildroot}%{_bindir}/rhelmon-start
install -D -m 0644 configs/rhelmon.service     %{buildroot}%{_unitdir}/rhelmon.service
install -D -m 0640 configs/rhelmon.conf        %{buildroot}%{_sysconfdir}/rhelmon/rhelmon.conf
install -D -m 0644 configs/rhelmon-logrotate   %{buildroot}%{_sysconfdir}/logrotate.d/rhelmon

# Plugin examples
install -D -m 0755 plugins/nginx_plugin.sh            %{buildroot}%{_sysconfdir}/rhelmon/plugins.examples/nginx_plugin.sh
install -D -m 0755 plugins/redis_plugin.sh            %{buildroot}%{_sysconfdir}/rhelmon/plugins.examples/redis_plugin.sh
install -D -m 0755 plugins/postgres_plugin.py         %{buildroot}%{_sysconfdir}/rhelmon/plugins.examples/postgres_plugin.py
install -D -m 0755 plugins/systemd_services_plugin.sh %{buildroot}%{_sysconfdir}/rhelmon/plugins.examples/systemd_services_plugin.sh

# Directories
install -d -m 0755 %{buildroot}%{_sysconfdir}/rhelmon/plugins
install -d -m 0750 %{buildroot}%{_localstatedir}/log/rhelmon

%pre
getent group  rhelmon >/dev/null || groupadd -r rhelmon
getent passwd rhelmon >/dev/null || \
  useradd -r -g rhelmon -d /var/lib/rhelmon -s /sbin/nologin \
  -c "rhelmon monitoring agent" rhelmon
exit 0

%post
%systemd_post rhelmon.service
echo ""
echo "  rhelmon %{version} installed successfully."
echo ""
echo "  Quick start:"
echo "    systemctl enable --now rhelmon"
echo "    firewall-cmd --add-port=9000/tcp --permanent && firewall-cmd --reload"
echo ""
echo "  Dashboard: http://$(hostname -I 2>/dev/null | awk '{print $1}' || echo 'your-server-ip'):9000"
echo "  Config:    /etc/rhelmon/rhelmon.conf"
echo "  Plugins:   /etc/rhelmon/plugins/"
echo "  Examples:  /etc/rhelmon/plugins.examples/"
echo "  Logs:      journalctl -u rhelmon -f"
echo ""

%preun
%systemd_preun rhelmon.service

%postun
%systemd_postun_with_restart rhelmon.service

%files
%defattr(-,root,root,-)
%license LICENSE
%doc README.md

%{_bindir}/rhelmon
%{_bindir}/rhelmon-start
%{_unitdir}/rhelmon.service

%config(noreplace) %attr(0640,root,rhelmon) %{_sysconfdir}/rhelmon/rhelmon.conf
%{_sysconfdir}/logrotate.d/rhelmon

%dir %attr(0755,root,root)      %{_sysconfdir}/rhelmon
%dir %attr(0755,rhelmon,rhelmon) %{_sysconfdir}/rhelmon/plugins
%dir %attr(0755,root,root)      %{_sysconfdir}/rhelmon/plugins.examples
%{_sysconfdir}/rhelmon/plugins.examples/

%dir %attr(0750,rhelmon,rhelmon) %{_localstatedir}/log/rhelmon

%changelog
* Wed Mar 18 2026 rhelmon <rhelmon@github.com> - 0.1.0-1
- Initial release
- Phase 1: Collector Manager, Ring Buffer (1hr history), WebSocket Hub, dashboard SPA
- Phase 2: Alert Engine (ok/pending/firing/resolved), Slack + email notifiers
- Phase 3: Prometheus remote write + InfluxDB line protocol TSDB writers
- Phase 4: External plugin loader with example plugins
- Phase 5: RPM packaging for RHEL/SUSE
- Phase 6: Self-monitor (goroutines, heap, GC, /metrics endpoint, watchdog)
