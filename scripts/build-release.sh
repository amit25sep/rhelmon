#!/bin/bash
# build-release.sh
# Builds the rhelmon RPM and all GitHub release assets.
#
# Prerequisites:
#   dnf install -y golang rpm-build rpmdevtools
#
# Usage:
#   ./build-release.sh                  # build version from VERSION file
#   ./build-release.sh --version 0.2.0  # override version
#   ./build-release.sh --skip-go        # skip Go compile, reuse existing binary
#
# Output (in ./dist/):
#   rhelmon-0.1.0-1.el9.x86_64.rpm     ← install on RHEL/Rocky/Alma
#   rhelmon-0.1.0-1.el9.src.rpm         ← source RPM
#   rhelmon-0.1.0.tar.gz                ← source tarball (GitHub release asset)
#   rhelmon-linux-amd64                 ← raw binary (GitHub release asset)
#   rhelmon-linux-arm64                 ← raw binary ARM (GitHub release asset)
#   SHA256SUMS                          ← checksums for all assets
#   install.sh                          ← one-line installer script

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
RPM_BUILD_DIR="$SCRIPT_DIR/rpm-build"
DIST_DIR="$SCRIPT_DIR/../dist"

VERSION="$(cat "$SCRIPT_DIR/../VERSION" 2>/dev/null || echo "0.1.0")"
SKIP_GO=false
GITHUB_REPO="amit25sep/rhelmon"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'
ok()   { echo -e "${GREEN}  ✓ $*${NC}"; }
info() { echo -e "${BLUE}  → $*${NC}"; }
warn() { echo -e "${YELLOW}  ! $*${NC}"; }
err()  { echo -e "${RED}  ✗ $*${NC}"; exit 1; }
step() { echo -e "\n${BOLD}[ $* ]${NC}"; }

# ── Parse args ────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case $1 in
    --version) VERSION="$2"; shift 2 ;;
    --skip-go) SKIP_GO=true; shift ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

echo ""
echo -e "${BOLD}╔══════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║   rhelmon release builder v${VERSION}        ║${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════╝${NC}"
echo ""

# ── Preflight checks ──────────────────────────────────────────────────────────
step "Preflight"
command -v rpmbuild >/dev/null || err "rpmbuild not found. Run: dnf install -y rpm-build rpmdevtools"
if [ "$SKIP_GO" = false ]; then
  command -v go >/dev/null || err "go not found. Run: dnf install -y golang"
  GO_VER=$(go version | awk '{print $3}' | tr -d 'go')
  ok "Go $GO_VER"
fi
ok "rpmbuild $(rpmbuild --version | head -1)"

mkdir -p "$DIST_DIR"
ok "dist dir: $DIST_DIR"

# ── Step 1: Build Go binaries ─────────────────────────────────────────────────
step "Building Go binaries"
if [ "$SKIP_GO" = false ]; then
  cd "$PROJECT_ROOT"
  LDFLAGS="-s -w -X main.version=$VERSION"

  info "Building linux/amd64..."
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$DIST_DIR/rhelmon-linux-amd64" ./cmd/rhelmon
  ok "rhelmon-linux-amd64 ($(du -sh "$DIST_DIR/rhelmon-linux-amd64" | cut -f1))"

  info "Building linux/arm64..."
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$LDFLAGS" -o "$DIST_DIR/rhelmon-linux-arm64" ./cmd/rhelmon
  ok "rhelmon-linux-arm64 ($(du -sh "$DIST_DIR/rhelmon-linux-arm64" | cut -f1))"
else
  warn "Skipping Go build — using existing binaries in $DIST_DIR"
  [ -f "$DIST_DIR/rhelmon-linux-amd64" ] || err "rhelmon-linux-amd64 not found in $DIST_DIR"
fi

# ── Step 2: Build source tarball ──────────────────────────────────────────────
step "Building source tarball"
TARBALL_DIR="$DIST_DIR/rhelmon-$VERSION"
TARBALL="$DIST_DIR/rhelmon-$VERSION.tar.gz"
rm -rf "$TARBALL_DIR"
mkdir -p \
  "$TARBALL_DIR/bin" \
  "$TARBALL_DIR/configs" \
  "$TARBALL_DIR/plugins"

# Binaries
cp "$DIST_DIR/rhelmon-linux-amd64" "$TARBALL_DIR/bin/"
cp "$DIST_DIR/rhelmon-linux-arm64" "$TARBALL_DIR/bin/"

# Configs (from rpm-build/SOURCES)
cp "$RPM_BUILD_DIR/SOURCES/rhelmon.conf"       "$TARBALL_DIR/configs/"
cp "$RPM_BUILD_DIR/SOURCES/rhelmon-start.sh"   "$TARBALL_DIR/configs/"
cp "$RPM_BUILD_DIR/SOURCES/rhelmon.service"     "$TARBALL_DIR/configs/"
cp "$RPM_BUILD_DIR/SOURCES/rhelmon-logrotate"   "$TARBALL_DIR/configs/"

# Plugins
PLUGIN_SRC="$PROJECT_ROOT/configs/plugins"
if [ -d "$PLUGIN_SRC" ]; then
  cp "$PLUGIN_SRC"/*.sh  "$TARBALL_DIR/plugins/" 2>/dev/null || true
  cp "$PLUGIN_SRC"/*.py  "$TARBALL_DIR/plugins/" 2>/dev/null || true
fi

# Docs
cp "$PROJECT_ROOT/README.md" "$TARBALL_DIR/" 2>/dev/null || echo "# rhelmon" > "$TARBALL_DIR/README.md"
echo "MIT" > "$TARBALL_DIR/LICENSE"

# Pack
cd "$DIST_DIR"
tar -czf "$TARBALL" "rhelmon-$VERSION/"
rm -rf "$TARBALL_DIR"
ok "$(basename $TARBALL) ($(du -sh "$TARBALL" | cut -f1))"

# ── Step 3: Build RPM ─────────────────────────────────────────────────────────
step "Building RPM"

# Stage tarball where rpmbuild expects it
cp "$TARBALL" "$RPM_BUILD_DIR/SOURCES/"

rpmbuild \
  --define "_topdir $RPM_BUILD_DIR" \
  --define "version $VERSION" \
  -ba "$RPM_BUILD_DIR/SPECS/rhelmon.spec" \
  2>&1 | grep -v "^Processing files\|^Checking\|^Wrote:" || true

# Copy RPMs to dist
find "$RPM_BUILD_DIR/RPMS" -name "*.rpm" -exec cp {} "$DIST_DIR/" \;
find "$RPM_BUILD_DIR/SRPMS" -name "*.rpm" -exec cp {} "$DIST_DIR/" \;

RPM_FILE=$(find "$DIST_DIR" -name "rhelmon-${VERSION}*.x86_64.rpm" | head -1)
[ -f "$RPM_FILE" ] || err "RPM not found after build"
ok "$(basename $RPM_FILE) ($(du -sh "$RPM_FILE" | cut -f1))"

# ── Step 4: Write install.sh (one-liner installer for GitHub releases) ─────────
step "Writing install.sh"
cat > "$DIST_DIR/install.sh" << INSTALLER
#!/bin/bash
# rhelmon one-line installer
# Usage:
#   bash <(curl -fsSL https://raw.githubusercontent.com/${GITHUB_REPO}/main/scripts/install.sh)
#
# Or with a specific version:
#   VERSION=0.1.0 bash <(curl -fsSL ...)

set -euo pipefail

VERSION="\${VERSION:-${VERSION}}"
GITHUB_REPO="${GITHUB_REPO}"
BASE_URL="https://github.com/\${GITHUB_REPO}/releases/download/v\${VERSION}"

RED='\033[0;31m'; GREEN='\033[0;32m'; BLUE='\033[0;34m'; NC='\033[0m'
ok()   { echo -e "\${GREEN}  ✓ \$*\${NC}"; }
info() { echo -e "\${BLUE}  → \$*\${NC}"; }
err()  { echo -e "\${RED}  ✗ \$*\${NC}"; exit 1; }

echo ""
echo "  Installing rhelmon v\${VERSION}..."
echo ""

# Detect OS and package manager
if command -v rpm &>/dev/null && command -v dnf &>/dev/null; then
  PKG_MGR="dnf"
elif command -v rpm &>/dev/null && command -v zypper &>/dev/null; then
  PKG_MGR="zypper"
elif command -v rpm &>/dev/null; then
  PKG_MGR="rpm"
else
  err "Unsupported OS — this installer requires an RPM-based Linux distribution."
fi
ok "Package manager: \$PKG_MGR"

# Detect arch
ARCH=\$(uname -m)
case \$ARCH in
  x86_64)  RPM_ARCH="x86_64" ;;
  aarch64) RPM_ARCH="aarch64" ;;
  *) err "Unsupported architecture: \$ARCH" ;;
esac

# Detect distro version for RPM filename
DIST_TAG=""
if [ -f /etc/os-release ]; then
  . /etc/os-release
  case \$ID in
    rhel|rocky|almalinux|centos) DIST_TAG="el\${VERSION_ID%%.*}" ;;
    opensuse*|sles)              DIST_TAG="suse" ;;
    *)                           DIST_TAG="el9" ;;
  esac
fi

RPM_FILE="rhelmon-\${VERSION}-1.\${DIST_TAG}.\${RPM_ARCH}.rpm"
RPM_URL="\${BASE_URL}/\${RPM_FILE}"

info "Downloading \${RPM_FILE}..."
TMP=\$(mktemp /tmp/rhelmon-XXXXXX.rpm)
curl -fsSL "\${RPM_URL}" -o "\$TMP" || err "Download failed: \${RPM_URL}"
ok "Downloaded"

info "Installing RPM..."
case \$PKG_MGR in
  dnf)    dnf install -y "\$TMP" ;;
  zypper) zypper --non-interactive install "\$TMP" ;;
  rpm)    rpm -Uvh "\$TMP" ;;
esac
rm -f "\$TMP"
ok "Installed"

echo ""
echo "  Next steps:"
echo "    systemctl enable --now rhelmon"
echo "    firewall-cmd --add-port=9000/tcp --permanent && firewall-cmd --reload"
echo "    vi /etc/rhelmon/rhelmon.conf"
echo ""
echo "  Dashboard: http://\$(hostname -I 2>/dev/null | awk '{print \$1}' || echo 'your-server'):9000"
echo ""
INSTALLER
chmod +x "$DIST_DIR/install.sh"
ok "install.sh written"

# ── Step 5: SHA256 checksums ──────────────────────────────────────────────────
step "Generating checksums"
cd "$DIST_DIR"
sha256sum \
  rhelmon-linux-amd64 \
  rhelmon-linux-arm64 \
  rhelmon-${VERSION}.tar.gz \
  $(basename $RPM_FILE) \
  > SHA256SUMS
ok "SHA256SUMS written"
cat SHA256SUMS

# ── Summary ───────────────────────────────────────────────────────────────────
step "Release assets ready"
echo ""
ls -lh "$DIST_DIR"
echo ""
echo -e "${GREEN}  Upload these files to your GitHub release:${NC}"
echo "    dist/rhelmon-linux-amd64"
echo "    dist/rhelmon-linux-arm64"
echo "    dist/rhelmon-${VERSION}.tar.gz"
echo "    dist/$(basename $RPM_FILE)"
echo "    dist/SHA256SUMS"
echo ""
echo -e "${GREEN}  Also commit this file to your repo root:${NC}"
echo "    scripts/install.sh  ← copy from dist/install.sh"
echo ""
echo -e "${GREEN}  Users can then install with:${NC}"
echo "    rpm -ivh https://github.com/${GITHUB_REPO}/releases/download/v${VERSION}/$(basename $RPM_FILE)"
echo ""
echo -e "${GREEN}  Or one-liner:${NC}"
echo "    bash <(curl -fsSL https://raw.githubusercontent.com/${GITHUB_REPO}/main/scripts/install.sh)"
echo ""
