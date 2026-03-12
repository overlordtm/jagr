# Jagr (The Hunter) — Autonomous Security Audit Agent v2.0

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
   │  │  Clean    │  │  ReAct    │ │
   │  │  Room     │  │  Loop     │ │
   │  │ (/dev/shm)│  │  Engine   │ │
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
- **Intelligent Tooling** — ReAct loop with LinPEAS, pspy, and custom tools

## Building

```bash
# Build both binaries
go build -o jagr-gateway ./cmd/gateway
go build -o jagr-agent ./cmd/agent

# Or build everything
go build ./...
```

## Gateway Server

### Configuration

Create a `gateway.yaml` file:

```yaml
server:
  listen: ":8443"
  tls:
    cert: /etc/jagr/server.crt
    key: /etc/jagr/server.key

database:
  path: /var/lib/jagr/jagr.db

rate_limit:
  requests_per_minute: 30
  max_concurrent: 5

session:
  timeout_minutes: 120
  history_mode: gateway

providers:
  - name: openrouter
    type: openai_compatible
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    models:
      - alias: "default"
        upstream: "anthropic/claude-sonnet-4"

default_provider: openrouter
default_model: default
```

### Running

```bash
# Start with default config
./jagr-gateway

# With custom config
./jagr-gateway -config custom.yaml

# Verbose logging
./jagr-gateway -verbose
```

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

## Agent

### Usage

```bash
jagr [flags]

Flags:
  --gateway-url    string    Gateway server URL (required)
  --api-key        string    API key for gateway auth (or JAGR_API_KEY env)
  --mode           string    Execution mode: batch | interactive (default: interactive)
  --max-iterations int       Maximum ReAct loop iterations (default: 50)
  --model          string    Model alias to request from gateway (default: "default")
  --objective      string    Custom objective prompt
  --output-dir     string    Directory for reports (default: ./jagr-output)
  --verbose        bool      Verbose local logging (default: false)
```

### Examples

```bash
# Run autonomous audit
JAGR_API_KEY=your-api-key ./jagr-agent --gateway-url https://gateway.example.com --mode batch

# Interactive mode with human oversight
./jagr-agent --gateway-url https://gateway.example.com --mode interactive

# Custom objective
./jagr-agent --gateway-url https://gateway.example.com --objective "Look for SSH backdoors in /root/.ssh"
```

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

- **BusyBox** — Static coreutils with symlink farm
- **LinPEAS** — Privilege escalation checks (shell + static)
- **pspy** — Process monitoring (amd64/32)

Tools are embedded via Go's `embed.FS` and extracted at runtime to the Clean Room.

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

## Developing

### Project Structure

```
jagr/
├── cmd/
│   ├── gateway/          # Gateway server entry point
│   └── agent/            # Agent entry point
├── internal/
│   ├── gateway/          # Gateway components
│   │   ├── db/           # SQLite storage
│   │   ├── models/       # Data models
│   │   ├── provider/     # LLM provider routing
│   │   └── ratelimit.go # Rate limiting
│   └── agent/            # Agent components
│       ├── cleanroom.go  # Trusted execution
│       ├── tools.go      # Tool definitions
│       └── agent.go      # ReAct engine
├── pkg/                  # Shared packages
├── embed/                # Embedded resources
├── gateway.yaml.example  # Config template
└── PLAN.md               # Architecture spec
```

### Adding New Tools

1. Add tool binary/file to `internal/agent/tools/`
2. Add tool to `GetAvailableTools()` in `internal/agent/tools.go`
3. Add execution handler in `internal/agent/agent.go`

### Adding New Providers

Implement the `provider.Provider` interface:

```go
type Provider interface {
    ChatCompletion(ctx context.Context, req models.ChatCompletionRequest) (*models.ChatCompletionResponse, error)
}
```

Add provider to config's `providers` array with appropriate type.

## License

Internal / Exercise Use Only

## Copyright

© 2026 Security Engineering Team
