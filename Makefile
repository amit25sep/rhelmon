BINARY   := rhelmon
CMD      := ./cmd/rhelmon
VERSION  := 0.1.0
LDFLAGS  := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build
build:
	go build $(LDFLAGS) -o $(BINARY) $(CMD)

.PHONY: build-rhel
build-rhel:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build $(LDFLAGS) -o $(BINARY)-linux-amd64 $(CMD)
	@echo "Binary: $(BINARY)-linux-amd64"
	@ls -lh $(BINARY)-linux-amd64

.PHONY: build-rhel-arm
build-rhel-arm:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	  go build $(LDFLAGS) -o $(BINARY)-linux-arm64 $(CMD)

.PHONY: run
run:
	go run $(CMD) -addr :9000

.PHONY: test
test:
	go test ./...

.PHONY: test-verbose
test-verbose:
	go test -v ./...

.PHONY: tidy
tidy:
	go mod tidy
	go mod verify

.PHONY: install
install: build-rhel
	install -m 755 $(BINARY)-linux-amd64 /usr/local/bin/$(BINARY)
	@echo "Installed to /usr/local/bin/$(BINARY)"

.PHONY: systemd-unit
systemd-unit:
	@cat configs/rhelmon.service

.PHONY: clean
clean:
	rm -f $(BINARY) $(BINARY)-linux-amd64 $(BINARY)-linux-arm64

# ── RPM packaging ─────────────────────────────────────────────────────────────
.PHONY: rpm
rpm: build-rhel
	./packaging/build-rpm.sh --no-build

.PHONY: rpm-full
rpm-full:
	./packaging/build-rpm.sh

.PHONY: rpm-install
rpm-install: rpm
	@RPM=$$(find packaging/rpm/RPMS -name "rhelmon-*.rpm" | head -1); \
	echo "Installing $$RPM ..."; \
	rpm -Uvh "$$RPM"
