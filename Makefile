.PHONY: all build build-agent build-gateway test clean tools fetch-tools patch-busybox

AGENT_BIN   := dist/jagr-agent
GATEWAY_BIN := dist/jagr-gateway
TOOLS_DIR   := internal/agent/tools
TOOLS_BIN   := $(TOOLS_DIR)/bin

# Tool versions (override via environment or make args)
BUSYBOX_VERSION  ?= 1.36.1
LINPEAS_VERSION  ?= 20250316
PSPY_VERSION     ?= 1.2.1

GOOS   ?= linux
GOARCH ?= amd64

all: fetch-tools build

# --- Build ---

build: build-agent build-gateway

build-agent:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(AGENT_BIN)-$(GOOS)-$(GOARCH) ./cmd/agent

build-gateway:
	@mkdir -p dist
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(GATEWAY_BIN)-$(GOOS)-$(GOARCH) ./cmd/gateway

# --- Fetch Tools (downloaded at build time, not committed to repo) ---

fetch-tools: fetch-busybox fetch-linpeas fetch-pspy

patch-busybox:
	@cd external/busybox && \
	for p in ../busybox-patches/*.patch; do \
		if git apply --check "$$p" 2>/dev/null; then \
			echo "Applying $$(basename $$p)..."; \
			git apply "$$p"; \
		else \
			echo "$$(basename $$p) already applied, skipping."; \
		fi; \
	done

fetch-busybox:
	@mkdir -p $(TOOLS_BIN)
	@if [ ! -f $(TOOLS_BIN)/busybox ] || [ "$$(wc -c < $(TOOLS_BIN)/busybox)" -lt 1024 ]; then \
		echo "Downloading BusyBox $(BUSYBOX_VERSION) ($(GOARCH))..."; \
		ARCH=$(GOARCH); \
		if [ "$$ARCH" = "amd64" ]; then ARCH="x86_64"; fi; \
		if [ "$$ARCH" = "arm64" ]; then ARCH="aarch64"; fi; \
		curl -fsSL --connect-timeout 10 --max-time 120 "https://busybox.net/downloads/binaries/$(BUSYBOX_VERSION)-defconfig-multiarch-musl/busybox-$$ARCH" \
			-o $(TOOLS_BIN)/busybox || { echo "ERROR: BusyBox download failed"; exit 1; }; \
		chmod +x $(TOOLS_BIN)/busybox; \
		echo "BusyBox downloaded."; \
	else \
		echo "BusyBox already present, skipping."; \
	fi

fetch-linpeas:
	@mkdir -p $(TOOLS_DIR)
	@if [ ! -f $(TOOLS_DIR)/linpeas.sh ] || [ "$$(wc -c < $(TOOLS_DIR)/linpeas.sh)" -lt 1024 ]; then \
		echo "Downloading LinPEAS..."; \
		curl -fsSL --connect-timeout 10 --max-time 120 "https://github.com/peass-ng/PEASS-ng/releases/latest/download/linpeas.sh" \
			-o $(TOOLS_DIR)/linpeas.sh || { echo "ERROR: LinPEAS download failed"; exit 1; }; \
		chmod +x $(TOOLS_DIR)/linpeas.sh; \
		echo "LinPEAS downloaded."; \
	else \
		echo "LinPEAS already present, skipping."; \
	fi

fetch-pspy:
	@mkdir -p $(TOOLS_DIR)
	@if [ ! -f $(TOOLS_DIR)/pspy64 ] || [ "$$(wc -c < $(TOOLS_DIR)/pspy64)" -lt 1024 ]; then \
		echo "Downloading pspy $(PSPY_VERSION)..."; \
		curl -fsSL --connect-timeout 10 --max-time 120 "https://github.com/DominicBreuker/pspy/releases/download/v$(PSPY_VERSION)/pspy64" \
			-o $(TOOLS_DIR)/pspy64 || { echo "ERROR: pspy64 download failed"; exit 1; }; \
		chmod +x $(TOOLS_DIR)/pspy64; \
		curl -fsSL --connect-timeout 10 --max-time 120 "https://github.com/DominicBreuker/pspy/releases/download/v$(PSPY_VERSION)/pspy32" \
			-o $(TOOLS_DIR)/pspy || { echo "ERROR: pspy32 download failed"; exit 1; }; \
		chmod +x $(TOOLS_DIR)/pspy; \
		echo "pspy downloaded."; \
	else \
		echo "pspy already present, skipping."; \
	fi

# --- Test ---

test:
	go test ./...

# --- Clean ---

clean:
	rm -rf dist/
