# Jagr (Jagr Autonomous Guardian Agent) — Autonomous Security Audit Agent v2.0

**Classification:** Internal / Exercise Use Only  
**Date:** March 2026

---

## Overview

Jagr is an autonomous, AI-driven security engineer designed for deep inspection of Linux systems within ephemeral cybersecurity exercise environments. It operates as a two-component system:

- **Agent** — A lightweight binary deployed on target hosts with embedded forensic tooling
- **Gateway Server** — Centralized server providing LLM intelligence, session management, and audit logging

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        GATEWAY SERVER                           │
│  ┌──────────┐  ┌──────────────┐  ┌───────────┐  ┌───────────┐  │
│  │ OpenAI   │  │   Session    │  │  Provider  │  │  Logging  │  │
│  │ Compat   │──│   Manager    │──│  Router    │──│  (SQLite) │  │
│  │ API      │  │  (stateful)  │  │ (pluggable)│  │           │  │
│  └──────────┘  └──────────────┘  └───────────┘  └───────────┘  │
│       ▲                                │                        │
└───────┼────────────────────────────────┼────────────────────────┘
        │ HTTPS + API Key               │ HTTPS
        │                               ▼
        │                     ┌──────────────────┐
        │                     │   LLM Provider   │
        │                     │ (OpenRouter,     │
        │                     │  Ollama, vLLM,   │
        │                     │  OpenAI, etc.)   │
        │                     └──────────────────┘
        │
   ─────┼──────────── Exercise Network ────────────────
        │
   ┌────┴──────────────────────────┐
   │         JAGR AGENT            │
   │  (target host, runs as root)  │
   │  ┌───────────┐  ┌───────────┐ │
   │  │  Clean    │  │  Multi-   │ │
   │  │  Room     │  │  Phase    │ │
   │  │ (/dev/shm)│  │  Harness  │ │
   │  └───────────┘  └───────────┘ │
   │  ┌───────────┐  ┌───────────┐ │
   │  │ Embedded  │  │  OpenAI   │ │
   │  │ Tools     │  │  Client   │ │
   │  │ (busybox, │  │           │ │
   │  │  linpeas) │  │           │ │
   │  └───────────┘  └───────────┘ │
   └────────────────────────────────┘
```

## Features

- **Trusted Execution** — Clean Room execution environment protects against host-level compromises
- **Self-Contained** — All forensic tools embedded in static Go binary
- **OpenAI-Compatible** — Works with any OpenAI-compatible LLM provider
- **Multi-Agent Support** — Central gateway manages multiple concurrent agents
- **Full Audit Trail** — SQLite-based logging of all activities
- **Intelligent Tooling** — Multi-phase parallel investigation with LinPEAS, pspy, and custom tools
- **Web Dashboard** — Real-time monitoring of exercises, agents, sessions, and cost tracking
- **Cost Tracking** — Automatic per-model pricing via OpenRouter with per-session cost aggregation
- **ACME / Auto-TLS** — Optional Let's Encrypt integration; auto-generates self-signed certs as fallback
- **Circuit Breaker** — Per-tool failure tracking prevents runaway loops on broken tools
- **Session Heartbeats** — Agents send periodic heartbeats so the gateway can detect stale sessions
- **Remote Execution** — Copy and execute agent on remote hosts over SSH with automatic reverse tunnel
- **Context Management** — Rolling-window summarization strategy keeps long sessions within token budgets
- **Host Enrichment** — Automatic pre-flight context collection (users, cron, SUID, listeners, systemd)
- **Knowledge Base (RAG)** — Optional document store for exercise baselines, ingestable via CLI
- **Production Builds** — xz-compressed embedded tools and stripped binaries for minimal binary size

## Building

```bash
# Fetch embedded tools and build both binaries
make all

# Or build individually
make build-agent
make build-gateway

# Production builds (stripped + xz-compressed tools, smallest binary)
make build-prod
make build-agent-prod
make build-gateway-prod

# Compress embedded tools separately
make compress-tools

# Fetch tools separately (BusyBox is built from source via submodule)
make fetch-tools
```

Binaries are output to `dist/`. The Makefile cross-compiles for Linux amd64 by default; override with `GOOS` and `GOARCH`.

The gateway build includes a Vite-based web dashboard (`web/`). The `build-gateway` target runs `build-dashboard` automatically.

## Gateway Server

### Configuration

The example config file is `gateway.example.yaml`. Copy and customize it:

```bash
cp gateway.example.yaml gateway.yaml
# Edit gateway.yaml as needed
```

Key sections:

```yaml
server:
  listen: ":8443"
  tls:
    cert: /etc/jagr/server.crt
    key: /etc/jagr/server.key
    # Optional ACME (Let's Encrypt) automatic TLS:
    # acme:
    #   enabled: true
    #   email: admin@example.com
    #   domains: [jagr.example.com]
    #   cache_dir: /var/lib/jagr/acme

database:
  path: /var/lib/jagr/jagr.db

rate_limit:
  requests_per_minute: 30
  max_concurrent: 5

session:
  timeout_minutes: 120
  max_tokens_per_session: 500000

# Shared API key for agent authentication (agents identify by hostname)
agent_api_key: ${JAGR_AGENT_API_KEY}

providers:
  - name: openrouter
    type: openai_compatible
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    models:
      - alias: "default"
        upstream: "anthropic/claude-sonnet-4"
        max_context_window: 200000
      - alias: "fast"
        upstream: "anthropic/claude-haiku-4"
        max_context_window: 200000
      - alias: "summarize"
        upstream: "google/gemini-flash-1.5"
        max_context_window: 1000000

default_provider: openrouter
default_model: default

dashboard:
  enabled: true
  listen: ":8080"
  # users:          # optional basic auth
  #   - username: admin
  #     password: changeme

# Agent profiles — override model/temperature per investigation phase
agents:
  phase_UserAccess:
    model: "fast"
    temperature: 0.1
    top_p: 0.95
  phase_Persistence:
    model: "fast"

# Knowledge base (RAG) for exercise documentation retrieval
# knowledge:
#   backend: chromem          # "chromem" (embedded) or "redis"
#   data_dir: /var/lib/jagr/knowledge
#   embedding:
#     provider: openai_compatible
#     model: text-embedding-3-small
#     base_url: https://openrouter.ai/api/v1
#     api_key: ${OPENROUTER_API_KEY}

# Skills directory — files become skills available to all agents
# skills_dir: /etc/jagr/skills

timeouts:
  provider_timeout_seconds: 300
  pricing_fetch_timeout_seconds: 30

logging:
  level: info
  audit: true
```

Environment variables are expanded in the config (e.g. `${OPENROUTER_API_KEY}`). Use a `.env` file with `direnv` or export them manually.

### Running

```bash
# Start with default config (gateway.yaml)
./jagr-gateway

# With custom config
./jagr-gateway --config custom.yaml

# Human-readable console logging (useful for development)
./jagr-gateway --config gateway.yaml --log-format console

# Verbose logging
./jagr-gateway --verbose

# Using direnv (loads .env automatically)
direnv exec . ./jagr-gateway --config gateway.yaml --log-format console
```

### Knowledge Base Ingest

Use the `ingest` subcommand to load documents into the RAG knowledge base:

```bash
# Ingest a directory of markdown files
./jagr-gateway ingest --config gateway.yaml /path/to/exercise-docs/

# Ingest specific files into a named collection
./jagr-gateway ingest --config gateway.yaml --collection network-baseline \
  /etc/network/interfaces /etc/hosts

# Control chunk size (characters per chunk, 0 = no chunking)
./jagr-gateway ingest --config gateway.yaml --chunk-size 4000 /path/to/docs/
```

Supported file types: `.md`, `.txt`, `.yaml`, `.yml`, `.json`, `.conf`, `.cfg`, `.ini`, `.toml`, `.sh`, `.py`, `.go`

### API Endpoints

#### Agent-Facing (OpenAI-compatible)

- `POST /v1/chat/completions` — Send messages and receive tool calls
- `GET /v1/models` — List available models
- `GET /health` — Health check

#### Admin API

- `GET /admin/exercises` — List exercises
- `GET /admin/exercises/{id}/agents` — List agents in exercise
- `GET /admin/agents/{id}/sessions` — List agent sessions
- `GET /admin/agents/{id}/sessions/{sid}/messages` — Get session messages
- `GET /admin/agents/{id}/logs` — Get audit logs

#### Web Dashboard

When `dashboard.enabled` is `true`, the gateway serves a web UI on the configured `dashboard.listen` address (default `:8080`). The dashboard provides real-time views of exercises, agents, sessions, messages, and cost summaries. Optional basic-auth can be configured via `dashboard.users`.

## Agent

### How It Works

The agent runs a **multi-phase parallel investigation**. After collecting host context (users, cron jobs, SUID binaries, open ports, systemd units), it spawns five concurrent phase agents:

| Phase | Focus |
|-------|-------|
| `UserAccess` | Backdoor accounts, sudo misconfigurations, SSH keys |
| `Persistence` | Cron, systemd timers, rc.local, init.d, shell profiles |
| `Network` | Firewall, open ports, routing, DNS, network config |
| `Filesystem` | SUID/SGID/capabilities, world-writable paths, LD_PRELOAD |
| `LogAnalysis` | Auth logs, wtmp, bash history, log tampering |

After all phases complete, a reporter agent consolidates findings into structured output.

### Usage

```bash
jagr-agent [flags]

Flags:
  --gateway-url          string   Gateway server URL (required) (env: JAGR_GATEWAY_URL)
  --api-key              string   API key for gateway auth (env: JAGR_API_KEY)
  --mode                 string   Execution mode: batch | interactive (default: interactive) (env: JAGR_MODE)
  --max-iterations       int      Maximum ReAct loop iterations per phase agent (default: 50) (env: JAGR_MAX_ITERATIONS)
  --max-tool-failures    int      Max consecutive failures per tool before circuit breaker trips (default: 5) (env: JAGR_MAX_TOOL_FAILURES)
  --model                string   Model alias to request from gateway (default: "default") (env: JAGR_MODEL)
  --objective            string   Custom objective prompt (env: JAGR_OBJECTIVE)
  --output-dir           string   Directory for reports; hostname subdirectory created automatically (default: ./jagr-output) (env: JAGR_OUTPUT_DIR)
  --verbose              bool     Verbose local logging (default: false) (env: JAGR_VERBOSE)
  --hostname             string   Override hostname detection (env: JAGR_HOSTNAME)
  --tls-skip-verify      bool     Skip TLS certificate verification (default: false) (env: JAGR_TLS_SKIP_VERIFY)
  --http-timeout         int      HTTP request timeout in seconds (default: 120) (env: JAGR_HTTP_TIMEOUT)
  --command-timeout       int     Default command execution timeout in seconds (default: 300) (env: JAGR_COMMAND_TIMEOUT)
  --long-command-timeout  int     Long-running command timeout in seconds (default: 900) (env: JAGR_LONG_COMMAND_TIMEOUT)
  --remote               string   SSH into this host, copy the agent binary there, and execute remotely (env: JAGR_REMOTE)
```

All flags can be set via environment variables (shown in parentheses above).

### Examples

```bash
# Run autonomous audit against local host
JAGR_API_KEY=your-api-key jagr-agent --gateway-url https://gateway.example.com --mode batch

# Interactive mode with human oversight
jagr-agent --gateway-url https://gateway.example.com --api-key $KEY --mode interactive

# Custom objective with self-signed gateway cert
jagr-agent --gateway-url https://gateway.example.com --api-key $KEY \
  --tls-skip-verify --objective "Look for SSH backdoors in /root/.ssh"

# Environment variables (alternative to flags)
export JAGR_GATEWAY_URL=https://gateway.example.com
export JAGR_API_KEY=your-api-key
export JAGR_TLS_SKIP_VERIFY=true
jagr-agent --mode batch

# Run agent on a remote host via SSH (binary is copied automatically)
# Reads ~/.ssh/config for host resolution; establishes reverse tunnel for gateway access
jagr-agent --gateway-url https://gateway.example.com --api-key $KEY \
  --mode batch --tls-skip-verify --remote vuln-box

# Remote execution using a specific SSH alias from ~/.ssh/config
jagr-agent --gateway-url https://gateway.example.com --api-key $KEY \
  --mode batch --tls-skip-verify --remote jagr-target
```

### Remote Execution

The `--remote <host>` flag enables agentless deployment: Jagr copies its own binary to the target over SSH and executes it there. A reverse SSH tunnel is automatically established so the remote agent can reach the gateway through the launching host — no direct network path from target to gateway is required.

Host resolution follows `~/.ssh/config` (Hostname, Port, User, IdentityFile). SSH agent and default key paths (`~/.ssh/id_rsa`, `id_ed25519`, `id_ecdsa`) are tried automatically. Artifacts are collected back to `--output-dir` after the run.

### Command Reference

| Tool | Arguments | Description |
|------|-----------|-------------|
| `execute_trusted` | command, args[] | Execute command in Clean Room |
| `read_file` | path, max_lines | Read file from filesystem |
| `write_file` | path, content | Write content to file |
| `get_system_env` | pid | Read process environment from /proc |
| `run_linpeas_sh` | flags | Execute LinPEAS shell script |
| `run_linpeas_static` | flags | Execute LinPEAS static binary |
| `run_pspy` | duration_seconds | Run process monitoring |
| `list_dir` | path, recursive | List directory contents |
| `search_files` | pattern, path, max_results | Search files with grep |
| `get_network_info` | — | Get network state |
| `submit_finding` | finding | Register a finding |
| `conclude` | summary | End investigation |

## Embedded Tools

- **BusyBox** — Static coreutils built from source (submodule in `external/busybox`) with symlink farm
- **LinPEAS** — Privilege escalation checks (downloaded at build time via `make fetch-tools`)
- **pspy** — Process monitoring (downloaded at build time)

Tools are embedded via Go's `embed.FS` and extracted at runtime to the Clean Room. Production builds (`build-agent-prod`) compress tools with xz (`-9`) before embedding to minimize binary size; decompression happens transparently at startup.

## Security Considerations

### Threat Model

**In Scope:**
- Userspace rootkits, PATH hijacking
- LD_PRELOAD injection, modified system binaries
- Malicious cron jobs, backdoor accounts

**Out of Scope:**
- Kernel-level rootkits (syscall hooking)
- Hardware-based attacks

### API Key Security

- Agent receives API key via environment variable or CLI flag
- API key held only in process memory
- Gateway stores hashed API keys (Argon2id/bcrypt)

### Network Recommendations

- Run gateway on same network segment as targets
- Use TLS with pinned certificates
- Consider non-standard ports for OPSEC

## Report Generation

At conclusion, the agent produces:

1. **findings.json** — OCSF-lite format (machine-readable)
2. **report.md** — Human-readable Markdown report
3. **jagr-events.jsonl** — Full local audit trail

Output is written to `<output-dir>/<hostname>/` so multiple targets can share a single output directory.

## Development

### Local Development Setup

The repo ships with `direnv` support. Copy `.env.example` to `.env`, fill in your API keys, and run `direnv allow`:

```bash
cp .env.example .env
# Set OPENROUTER_API_KEY in .env
direnv allow
```

Port forwarding is pre-configured in `.devcontainer/devcontainer.json`:

| Port | Service |
|------|---------|
| 8443 | JAGR Gateway API |
| 8080 | JAGR Dashboard |
| 5173 | Vite Dev Server |

### Running Locally (VS Code Tasks)

The following tasks are defined in `.vscode/tasks.json` and can be run via **Terminal → Run Task**:

```bash
# Start the gateway (loads env via direnv, human-readable logs)
direnv exec . go run ./cmd/gateway --config gateway.yaml --log-format console

# Run agent against local gateway (batch mode, skip TLS verify for dev cert)
go run ./cmd/agent --gateway-url https://127.0.0.1:8443 --api-key jagr-dev-key \
  --mode batch --tls-skip-verify

# Run agent targeting a remote host named "vuln-box" (via SSH)
go run ./cmd/agent/ --gateway-url https://127.0.0.1:8443 --api-key jagr-dev-key \
  --mode batch --tls-skip-verify --remote vuln-box

# Run agent targeting the Vagrant VM "jagr-target"
go run ./cmd/agent/ --gateway-url https://127.0.0.1:8443 --api-key jagr-dev-key \
  --mode batch --tls-skip-verify --remote jagr-target

# Start Vite dev server for dashboard development
cd web && npm run dev
```

### Vagrant Test Target

A `Vagrantfile` is included to spin up a vulnerable Ubuntu 22.04 VM for local testing:

```bash
vagrant up        # provision jagr-target at 172.28.128.3
vagrant ssh       # inspect the target manually
vagrant destroy   # tear down
```

The VM is provisioned by `scripts/setup-vulns.sh`, which plants ~50 vulnerabilities across users, persistence, SUID, file permissions, credentials, network, and more. See [scripts/README.md](scripts/README.md) for the full list.

### Project Structure

```
jagr/
├── cmd/
│   ├── gateway/            # Gateway server entry point
│   └── agent/              # Agent entry point
├── internal/
│   ├── gateway/            # Gateway components
│   │   ├── db/             # SQLite storage + migrations
│   │   ├── models/         # Data models & config
│   │   ├── provider/       # LLM provider routing & pricing
│   │   ├── knowledge/      # RAG knowledge base (chromem backend)
│   │   └── dashboard/      # Web dashboard (embedded HTML/JS/CSS)
│   └── agent/              # Agent components
│       ├── harness.go      # Multi-phase investigation orchestrator
│       ├── aiagent.go      # Per-phase ReAct engine
│       ├── cleanroom.go    # Trusted execution environment
│       ├── remote.go       # SSH remote execution + reverse tunnel
│       ├── gateway_client.go # Gateway HTTP client
│       ├── tools.go        # Tool definitions
│       ├── toolbox.go      # Tool execution handlers
│       ├── prompts/        # Embedded prompt templates (per phase/role)
│       ├── enrichment/     # Host context collection modules
│       ├── context_budget.go   # Tool output truncation
│       ├── context_strategy.go # Rolling-window summarization
│       └── findings_store.go   # Finding accumulation
├── external/
│   └── busybox/            # BusyBox submodule (built from source)
├── web/                    # Dashboard frontend (Vite + JS)
├── scripts/
│   ├── setup-vulns.sh      # Vagrant provisioner — plants vulnerabilities
│   └── README.md           # Vulnerability catalogue
├── Vagrantfile             # Vagrant VM for local testing
├── Makefile                # Build, fetch-tools, cross-compile
├── gateway.example.yaml    # Config template (was gateway.yaml.example)
├── .envrc                  # direnv: loads .env, sets up Go layout
└── .env.example            # Environment variable template
```

### Adding New Tools

1. Add tool binary/file to `internal/agent/tools/`
2. Add tool to `GetAvailableTools()` in `internal/agent/tools.go`
3. Add execution handler in `internal/agent/toolbox.go`

### Adding New Providers

Implement the `provider.Provider` interface:

```go
type Provider interface {
    ChatCompletion(ctx context.Context, req models.ChatCompletionRequest) (*models.ChatCompletionResponse, error)
}
```

Add provider to config's `providers` array with appropriate type.

### Adding New Investigation Phases

1. Create a prompt file at `internal/agent/prompts/phase_<Name>.md`
2. Add `"<Name>"` to the `phases` slice in `internal/agent/harness.go`
3. Optionally add an agent profile in `gateway.yaml` under `agents.phase_<Name>`

## License

Internal / Exercise Use Only

## Copyright

© 2026 Security Engineering Team
