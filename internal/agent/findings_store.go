package agent

import (
	"fmt"
	"strings"
	"sync"
)

// FindingsStore provides thread-safe storage and management of security findings.
// It is shared across all AiAgents during an investigation.
type FindingsStore struct {
	findings []Finding
	mu       sync.Mutex
}

// NewFindingsStore creates a new empty FindingsStore.
func NewFindingsStore() *FindingsStore {
	return &FindingsStore{}
}

// Add stores a new finding, setting its AgentName and auto-assigning an ID if empty.
// Returns the finding (with ID assigned) and true if it was a duplicate (by Observable).
// Duplicates are not added to the store.
func (fs *FindingsStore) Add(finding Finding, agentName string) (Finding, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	finding.AgentName = agentName

	// Deduplicate by observable
	for _, existing := range fs.findings {
		if existing.Observable == finding.Observable {
			return existing, true
		}
	}

	if finding.ID == "" {
		finding.ID = fmt.Sprintf("finding-%d", len(fs.findings)+1)
	}
	finding.Status = "preliminary"
	fs.findings = append(fs.findings, finding)
	return finding, false
}

// GetAll returns a copy of all findings.
func (fs *FindingsStore) GetAll() []Finding {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	result := make([]Finding, len(fs.findings))
	copy(result, fs.findings)
	return result
}

// Validate performs deduplication and validation on all findings, returning
// status updates suitable for sending to the gateway. It also updates the
// internal finding statuses in place.
func (fs *FindingsStore) Validate() []StatusUpdate {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	seen := map[string]string{} // observable -> finding ID of first occurrence
	var updates []StatusUpdate

	for i := range fs.findings {
		f := &fs.findings[i]
		if firstID, exists := seen[f.Observable]; exists {
			f.Status = "duplicate"
			_ = firstID // used for logging by caller if needed
		} else if f.Type == "incomplete_investigation" && f.Severity == "info" {
			f.Status = "invalid"
		} else {
			f.Status = "valid"
		}
		seen[f.Observable] = f.ID
		updates = append(updates, StatusUpdate{FindingID: f.ID, Status: f.Status})
	}

	return updates
}

// GetSummary returns a count of findings by severity.
func (fs *FindingsStore) GetSummary() FindingSummary {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	counts := map[string]int{}
	for _, f := range fs.findings {
		counts[f.Severity]++
	}
	return FindingSummary{
		Critical: counts["critical"],
		High:     counts["high"],
		Medium:   counts["medium"],
		Low:      counts["low"],
		Info:     counts["info"],
	}
}

// GetByAgent returns all findings submitted by the named agent.
func (fs *FindingsStore) GetByAgent(agentName string) []Finding {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	var result []Finding
	for _, f := range fs.findings {
		if f.AgentName == agentName {
			result = append(result, f)
		}
	}
	return result
}

// FilterSelfArtifacts marks any finding whose Observable falls under workDir
// as invalid, preventing jagr's own tools from being reported as malicious.
// Returns status updates suitable for sending to the gateway.
func (fs *FindingsStore) FilterSelfArtifacts(workDir string) []StatusUpdate {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if !strings.HasSuffix(workDir, "/") {
		workDir += "/"
	}
	dir := strings.TrimSuffix(workDir, "/")

	var updates []StatusUpdate
	for i := range fs.findings {
		f := &fs.findings[i]
		if strings.HasPrefix(f.Observable, workDir) || f.Observable == dir {
			f.Status = "invalid"
			updates = append(updates, StatusUpdate{FindingID: f.ID, Status: "invalid"})
		}
	}
	return updates
}

// GetReportable returns findings that are not marked invalid, for use by the
// reporter agent so jagr's own artifacts are excluded from the narrative.
func (fs *FindingsStore) GetReportable() []Finding {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	var result []Finding
	for _, f := range fs.findings {
		if f.Status != "invalid" {
			result = append(result, f)
		}
	}
	return result
}

// Count returns the number of stored findings.
func (fs *FindingsStore) Count() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.findings)
}
