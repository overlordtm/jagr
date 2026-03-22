.PHONY: all build build-agent build-gateway build-dashboard build-prod build-agent-prod build-gateway-prod compress-tools test clean tools fetch-tools patch-busybox build-busybox

AGENT_BIN   := dist/jagr-agent
GATEWAY_BIN := dist/jagr-gateway
TOOLS_DIR   := internal/agent/tools

# Tool versions (override via environment or make args)
BUSYBOX_VERSION  ?= 1.36.1
LINPEAS_VERSION  ?= 20250316
PSPY_VERSION     ?= 1.2.1

GOOS   ?= linux
GOARCH ?= amd64

PROD_FLAGS := -trimpath -ldflags="-s -w"

all: fetch-tools build

# --- Build ---

build: build-agent build-gateway

build-agent:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(AGENT_BIN)-$(GOOS)-$(GOARCH) ./cmd/agent

build-dashboard:
	cd web && npm install && npx vite build
	cp web/dist/assets/main.js internal/gateway/dashboard/static/assets/main.js
	cp web/dist/assets/main.css internal/gateway/dashboard/static/assets/main.css

build-gateway: build-dashboard
	@mkdir -p dist
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(GATEWAY_BIN)-$(GOOS)-$(GOARCH) ./cmd/gateway

# --- Compress embedded tools with xz ---

compress-tools:
	@for f in $(TOOLS_DIR)/busybox $(TOOLS_DIR)/linpeas.sh $(TOOLS_DIR)/pspy $(TOOLS_DIR)/pspy64; do \
		if [ -f "$$f" ] && [ ! -f "$$f.xz" -o "$$f" -nt "$$f.xz" ]; then \
			echo "Compressing $$(basename $$f) with xz..."; \
			xz -9 -k -f "$$f"; \
		fi; \
	done

# --- Production Build (smallest binaries) ---

build-prod: build-agent-prod build-gateway-prod

build-agent-prod: compress-tools
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(PROD_FLAGS) -tags xzcompress -o $(AGENT_BIN)-$(GOOS)-$(GOARCH) ./cmd/agent

build-gateway-prod: build-dashboard
	@mkdir -p dist
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(PROD_FLAGS) -o $(GATEWAY_BIN)-$(GOOS)-$(GOARCH) ./cmd/gateway

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

build-busybox: patch-busybox
	@cp external/busybox-config external/busybox/.config
	@$(MAKE) -C external/busybox -j$$(nproc)

fetch-busybox: build-busybox
	@mkdir -p $(TOOLS_DIR)
	@if [ ! -f $(TOOLS_DIR)/busybox ] || [ external/busybox/busybox -nt $(TOOLS_DIR)/busybox ]; then \
		echo "Copying BusyBox binary to $(TOOLS_DIR)..."; \
		cp external/busybox/busybox $(TOOLS_DIR)/busybox; \
		echo "BusyBox updated."; \
	else \
		echo "BusyBox already up to date, skipping."; \
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
