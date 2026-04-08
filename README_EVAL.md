# Jagr Eval Harness

The eval harness runs the Jagr agent against a target with multiple model/parameter configurations and produces a scored comparison report. This lets you answer questions like:

- Does Claude Sonnet find significantly more findings than Haiku, and is the cost worth it?
- Does lowering temperature improve precision?
- Which phases benefit most from a stronger model?

## How It Works

1. You define **variants** — named sets of per-agent-role model/temperature overrides — in an eval YAML file.
2. You define **ground truth** — a list of expected findings for your target host — in a separate YAML file.
3. `jagr-eval run` spawns the gateway and agent as subprocesses once per variant (× repeat count), each with a fresh isolated gateway config. Results are written to the gateway's SQLite database.
4. Findings are scored against ground truth using exact + fuzzy matching. Recall, Precision, F1, and false-positive rate are computed per variant.
5. A markdown comparison table is emitted.

## Prerequisites

Build all three binaries:

```bash
make build-agent build-gateway build-eval
```

Binaries will be in `dist/`:
- `dist/jagr-gateway-linux-amd64`
- `dist/jagr-agent-linux-amd64`
- `dist/jagr-eval-linux-amd64`

You also need a working `gateway.yaml` with at least one provider configured. Copy and edit the example:

```bash
cp gateway.example.yaml gateway.yaml
# Set OPENROUTER_API_KEY and JAGR_AGENT_API_KEY, or inline the values
```

## Quickstart

### 1. Write a ground truth file

The ground truth lists the findings you expect a correct audit to produce for your target. Each entry has a type, severity, and observable (the canonical identifier — a file path, username, IP:port, etc.).

```yaml
# testdata/ground-truth.vuln-box.yaml
target: "vuln-box"
findings:
  - type: persistence
    severity: critical
    observable: "/etc/cron.d/debug-cleanup"
    required: true
    notes: "Reverse shell cron running as root every 5 min"

  - type: privilege_escalation
    severity: critical
    observable: "/tmp/.suid-bash"
    required: true
    notes: "SUID bash copy"
```

A full ground truth for the `scripts/setup-vulns.sh` box is at [testdata/ground-truth.vuln-box.yaml](testdata/ground-truth.vuln-box.yaml).

### 2. Write an eval config

```yaml
# my-eval.yaml
name: "haiku-vs-sonnet"
ground_truth: "testdata/ground-truth.vuln-box.yaml"
repeat: 2  # run each variant twice and average

variants:
  - id: "fast"
    description: "Haiku 4.5 everywhere, low temperature"
    agents:
      phase_UserAccess:  { model: fast, temperature: 0.05 }
      phase_Persistence: { model: fast, temperature: 0.05 }
      phase_Network:     { model: fast, temperature: 0.05 }
      phase_Filesystem:  { model: fast, temperature: 0.05 }
      phase_LogAnalysis: { model: fast, temperature: 0.05 }
      reporter:          { model: fast, temperature: 0.3 }

  - id: "sonnet"
    description: "Claude Sonnet 4.6 everywhere"
    agents:
      phase_UserAccess:  { model: default, temperature: 0.1 }
      phase_Persistence: { model: default, temperature: 0.1 }
      phase_Network:     { model: default, temperature: 0.1 }
      phase_Filesystem:  { model: default, temperature: 0.1 }
      phase_LogAnalysis: { model: default, temperature: 0.1 }
      reporter:          { model: default, temperature: 0.2 }
```

See [eval.example.yaml](eval.example.yaml) for a three-variant example including a mixed config.

### 3. Run against the vuln-box target

The target in `ssh_config` is `vuln-box` at `192.168.121.27`. The agent uses the SSH remote mode:

```bash
jagr-eval run \
  --eval=my-eval.yaml \
  --gateway-config=gateway.yaml \
  --db=jagr.db \
  --agent-bin=dist/jagr-agent-linux-amd64 \
  --gateway-bin=dist/jagr-gateway-linux-amd64 \
  --api-key="${JAGR_AGENT_API_KEY}" \
  --target="ssh://root@192.168.121.27" \
  --hostname=vuln-box \
  --output=results/
```

The `--hostname` flag must match the `target` field in your ground truth file (used to correlate the DB session with the eval run).

For a local target (agent runs on the same machine as the gateway):

```bash
jagr-eval run \
  --eval=my-eval.yaml \
  --gateway-config=gateway.yaml \
  --db=jagr.db \
  --agent-bin=dist/jagr-agent-linux-amd64 \
  --gateway-bin=dist/jagr-gateway-linux-amd64 \
  --api-key="${JAGR_AGENT_API_KEY}" \
  --target=local \
  --output=results/
```

### 4. Read the report

`jagr-eval run` writes two files to `--output`:

- `eval-<id>-report.md` — human-readable markdown comparison table
- `eval-<id>-results.json` — machine-readable JSON (with `--json`)

Example output:

```
# Eval: haiku-vs-sonnet

**Date:** 2026-04-08T21:00:00Z
**Target:** vuln-box
**Ground truth findings:** 65

| Metric              | fast (n=2) | sonnet (n=2) |
|---------------------|------------|--------------|
| Recall              | 0.612      | 0.847        |
| Precision           | 0.791      | 0.882        |
| F1                  | 0.690      | 0.864        |
| False Positive Rate | 0.209      | 0.118        |
| Findings (avg)      | 38.0       | 62.5         |
| Cost USD (avg)      | $0.0412    | $0.3180      |
| Tokens In (avg)     | 44,200     | 101,300      |
| Tokens Out (avg)    | 9,100      | 21,400       |
| Duration s (avg)    | 94         | 218          |
```

The report then lists per-variant detail sections: missed findings, false positives, and matched findings with their scores.

## Other Commands

### Regenerate report from stored results

If you want to re-render the markdown without re-running agents:

```bash
jagr-eval report \
  --db=jagr.db \
  --eval-run=eval-9d465d55 \
  --ground-truth=testdata/ground-truth.vuln-box.yaml
```

### Re-score after updating ground truth

If you add new expected findings to the ground truth YAML after a run:

```bash
jagr-eval score \
  --db=jagr.db \
  --eval-run=eval-9d465d55 \
  --ground-truth=testdata/ground-truth.vuln-box.yaml
```

This re-computes Recall/Precision/F1 against the updated list without re-running the agents.

### Dry run

Preview which variants would be executed without spawning anything:

```bash
jagr-eval run --eval=my-eval.yaml --dry-run
```

## Scoring Details

Findings are matched against ground truth by the `observable` field (file path, username, IP:port, sysctl key, etc.).

| Match type | Score multiplier |
|-----------|-----------------|
| Exact observable + correct severity | 1.00 |
| Exact observable + wrong severity | 0.75 |
| Fuzzy observable (token Jaccard ≥ 0.75) + correct severity | 0.80 |
| Fuzzy observable + wrong severity | 0.60 |
| No match | false positive |

Token Jaccard splits observables on `/`, `-`, `_`, `.`, `:` and computes set intersection/union, so `/etc/cron.d/evil-cleanup` and `/etc/cron.d/cleanup` match (shared tokens `etc`, `cron`, `d`, `cleanup`), but `/etc/cron.d/cleanup` and `/etc/cron.weekly/rotate` do not.

Each ground truth item is matched at most once (greedy, best-score-first).

**Recall** = matched GT items / total GT items (did it find what matters?)  
**Precision** = matched findings / total agent findings (how noisy is it?)  
**F1** = harmonic mean — the primary quality metric  
**FP rate** = unmatched findings / total agent findings

## Ground Truth Tips

- Set `required: true` on findings that represent the most critical security issues. This makes it easy to scan the detail section for missed required findings.
- The `observable` field is the match key — use the most specific identifier: a full path for files, username for accounts, `host:port` for listeners, `sysctl.key` for kernel params.
- After each eval run, review the false positives section — if the agent consistently finds something real that isn't in your ground truth, add it.
- Start with `repeat: 1` for fast iteration; use `repeat: 3` or higher when you need statistically stable averages (LLM outputs are non-deterministic).
