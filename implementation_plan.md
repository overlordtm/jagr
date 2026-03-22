# Subagent Architecture Implementation Plan

This plan details the migration of the monolithic ReAct loop in Jagr to a structured multi-agent architecture based on Phase Agents, Investigator Agents, and a Reporter Agent.

## Architecture Concept

1. **Coordinator**: The main entry point replaces the monolithic ReAct loop. It sequentially iterates through the predefined audit phases (Phase 2-6) and launches a **Phase Agent** for each.
2. **Phase Agents**: Fixed, specialized agents with tightly scoped prompts (e.g., "You are the User & Access Audit Agent. Here is your checklist..."). They gather general facts during their assigned phase.
3. **Investigator Agents**: Dynamically spawned agents. If a Phase Agent discovers an anomaly (e.g., an unexpected SUID binary), it uses a new tool (`delegate_investigation`) to spawn an Investigator. The Investigator is given the anomaly context, runs deeper forensic commands (like `strings`, `strace`, [ss](file:///workspaces/jagr/internal/agent/agent.go#41-46)), and ultimately calls `submit_finding`.
4. **Reporter Agent**: Once all Phase Agents finish, the Coordinator passes all collected findings to the final Reporter Agent, which synthesizes a human-readable `report.md` using the `write_file` tool.

## Proposed Changes

### Core Subagent Implementation

#### [NEW] `internal/agent/subagent.go`
Introduce a `SubAgent` struct that handles isolated ReAct loops for specific roles.
- `SubAgent` will track its own `conversation` history, preventing context bloat across different phases.
- It will accept an `Objective` and [SystemPrompt](file:///workspaces/jagr/internal/agent/agent.go#295-529) on initialization.

#### [NEW] `internal/agent/prompts.go`
Extract the massive [buildSystemPrompt()](file:///workspaces/jagr/internal/agent/agent.go#295-529) string from [agent.go](file:///workspaces/jagr/internal/agent/agent.go) and split it into discrete, highly-focused templates:
- `PhaseUserAccessPrompt`
- `PhasePersistencePrompt`
- `PhaseNetworkPrompt`
- `PhaseFilesystemPrompt`
- `PhaseLogAnalysisPrompt`
- `InvestigatorPrompt`
- `ReporterPrompt`

### Tool Registry & Delegation

#### [MODIFY] [internal/agent/tools.go](file:///workspaces/jagr/internal/agent/tools.go)
- **Tool Scoping**: Modify the tool system so that each `SubAgent` only receives tools relevant to its role (e.g., the Reporter Agent only needs `write_file` and `read_file`; it doesn't need `execute_trusted`).
- **[NEW TOOL] `delegate_investigation`**: A tool available to Phase Agents.
    - Arguments: `target` (string), `context` (string)
    - Description: "Spawn an Investigator Agent to drill deeply into a specific suspicious file, process, or configuration."

### Coordinator Adjustments

#### [MODIFY] [internal/agent/agent.go](file:///workspaces/jagr/internal/agent/agent.go)
- Refactor `Agent.Run()` to become a strict Coordinator.
- Iterate over the phases, spawning a `SubAgent` for each.
- Maintain a global shared state for `findings`.
- After all phases complete, initialize the Reporter `SubAgent` with the global findings and instruct it to write `report.md`.

## Verification Plan

### Automated / Unit Verification
- Compile the updated binaries (`make build-agent`).
- Ensure the gateway still correctly tracks token usage accurately, as token metrics will now be aggregating across multiple independent subagent chat completions.

### Manual Verification
- Run `jagr-agent` in interactive mode to monitor the new multi-agent flow.
- Verify that standard Phase Agents (e.g., User & Access) do not bloat their context with output from later phases.
- Verify that finding anomalies successfully triggers the `delegate_investigation` tool.
- Confirm `report.md` generation summarizes the collective findings successfully.
