#!/bin/bash
# rhelmon installer
#
# Usage — install latest release:
#   bash <(curl -fsSL https://raw.githubusercontent.com/amit25sep/rhelmon/main/scripts/install.sh)
#
# Install specific version:
#   VERSION=0.1.0 bash <(curl -fsSL https://raw.githubusercontent.com/amit25sep/rhelmon/main/scripts/install.sh)
#
# Supported:
#   RHEL 8/9, Rocky Linux 8/9, AlmaLinux 8/9
#   CentOS Stream 8/9
#   openSUSE Leap 15, SLES 15

set -euo pipefail

GITHUB_REPO="amit25sep/rhelmon"
LATEST_VERSION="0.1.0"
VERSION="${VERSION:-$LATEST_VERSION}"
BASE_URL="https://github.com/${GITHUB_REPO}/releases/download/v${VERSION}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'
ok()   { echo -e "${GREEN}  ✓ $*${NC}"; }
info() { echo -e "${BLUE}  → $*${NC}"; }
warn() { echo -e "${YELLOW}  ! $*${NC}"; }
err()  { echo -e "${RED}  ✗ $*${NC}"; exit 1; }

# ── Root check ────────────────────────────────────────────────────────────────
[ "$EUID" -eq 0 ] || err "Please run as root: sudo bash <(curl ...)"

echo ""
echo -e "${BOLD}  rhelmon v${VERSION} installer${NC}"
echo ""

# ── Detect OS ─────────────────────────────────────────────────────────────────
info "Detecting OS..."
if [ ! -f /etc/os-release ]; then
  err "/etc/os-release not found — cannot detect OS"
fi
. /etc/os-release

DIST_MAJOR="${VERSION_ID%%.*}"
case "$ID" in
  rhel)        DIST_TAG="el${DIST_MAJOR}"; PKG_MGR="dnf" ;;
  rocky)       DIST_TAG="el${DIST_MAJOR}"; PKG_MGR="dnf" ;;
  almalinux)   DIST_TAG="el${DIST_MAJOR}"; PKG_MGR="dnf" ;;
  centos)      DIST_TAG="el${DIST_MAJOR}"; PKG_MGR="dnf" ;;
  fedora)      DIST_TAG="el9";             PKG_MGR="dnf" ;;
  opensuse*)   DIST_TAG="suse";            PKG_MGR="zypper" ;;
  sles)        DIST_TAG="suse";            PKG_MGR="zypper" ;;
  *) warn "Unknown distro '$ID' — attempting el9 RPM"; DIST_TAG="el9"; PKG_MGR="rpm" ;;
esac
ok "Detected: $PRETTY_NAME (tag: $DIST_TAG, pkg: $PKG_MGR)"

# ── Detect arch ───────────────────────────────────────────────────────────────
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  RPM_ARCH="x86_64" ;;
  aarch64) RPM_ARCH="aarch64" ;;
  *) err "Unsupported architecture: $ARCH (supported: x86_64, aarch64)" ;;
esac
ok "Architecture: $ARCH"

# ── Download RPM ──────────────────────────────────────────────────────────────
RPM_FILE="rhelmon-${VERSION}-1.${DIST_TAG}.${RPM_ARCH}.rpm"
RPM_URL="${BASE_URL}/${RPM_FILE}"

info "Downloading ${RPM_FILE}..."
info "From: ${RPM_URL}"

TMP=$(mktemp /tmp/rhelmon-XXXXXX.rpm)
trap "rm -f $TMP" EXIT

if command -v curl &>/dev/null; then
  curl -fsSL --progress-bar "$RPM_URL" -o "$TMP"
elif command -v wget &>/dev/null; then
  wget -q --show-progress "$RPM_URL" -O "$TMP"
else
  err "Neither curl nor wget found. Install one and retry."
fi
ok "Downloaded $(du -sh $TMP | cut -f1)"

# ── Verify checksum (if SHA256SUMS is reachable) ──────────────────────────────
info "Verifying checksum..."
SUMS_URL="${BASE_URL}/SHA256SUMS"
SUMS_TMP=$(mktemp /tmp/rhelmon-sums-XXXXXX)
if curl -fsSL "$SUMS_URL" -o "$SUMS_TMP" 2>/dev/null; then
  EXPECTED=$(grep "$RPM_FILE" "$SUMS_TMP" | awk '{print $1}')
  ACTUAL=$(sha256sum "$TMP" | awk '{print $1}')
  if [ "$EXPECTED" = "$ACTUAL" ]; then
    ok "Checksum verified"
  else
    rm -f "$SUMS_TMP"
    err "Checksum mismatch! Expected: $EXPECTED  Got: $ACTUAL"
  fi
  rm -f "$SUMS_TMP"
else
  warn "Could not fetch SHA256SUMS — skipping checksum verification"
fi

# ── Install ───────────────────────────────────────────────────────────────────
info "Installing with $PKG_MGR..."
case "$PKG_MGR" in
  dnf)
    # Check if already installed
    if rpm -q rhelmon &>/dev/null; then
      info "Upgrading existing installation..."
      dnf upgrade -y "$TMP"
    else
      dnf install -y "$TMP"
    fi
    ;;
  zypper)
    if rpm -q rhelmon &>/dev/null; then
      zypper --non-interactive update "$TMP"
    else
      zypper --non-interactive install "$TMP"
    fi
    ;;
  rpm)
    rpm -Uvh "$TMP"
    ;;
esac
ok "rhelmon ${VERSION} installed"

# ── Post-install guidance ─────────────────────────────────────────────────────
IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "your-server-ip")

echo ""
echo -e "${BOLD}  Installation complete!${NC}"
echo ""
echo "  Start the service:"
echo "    systemctl enable --now rhelmon"
echo ""
echo "  Open the firewall port (if firewalld is running):"
echo "    firewall-cmd --add-port=9000/tcp --permanent && firewall-cmd --reload"
echo ""
echo "  Configure Slack/TSDB/Prometheus (optional):"
echo "    vi /etc/rhelmon/rhelmon.conf"
echo "    systemctl restart rhelmon"
echo ""
echo "  Dashboard:  http://${IP}:9000"
echo "  Alerts:     http://${IP}:9000/api/alerts"
echo "  Prometheus: http://${IP}:9000/metrics"
echo "  Logs:       journalctl -u rhelmon -f"
echo ""
