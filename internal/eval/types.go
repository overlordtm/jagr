package eval

import (
	"time"

	"github.com/overlordtm/jagr/internal/gateway/models"
)

// EvalConfig defines an evaluation run: a set of variants to compare against a ground truth.
type EvalConfig struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description,omitempty"`
	GroundTruth string    `yaml:"ground_truth"`
	Variants    []Variant `yaml:"variants"`
	Repeat      int       `yaml:"repeat,omitempty"` // runs per variant, default 1
}

// Variant is a single configuration to test — a named override of agent profiles.
type Variant struct {
	ID          string                        `yaml:"id"`
	Description string                        `yaml:"description,omitempty"`
	Agents      map[string]models.AgentProfile `yaml:"agents"`
}

// GroundTruth lists the expected findings for a specific target host.
type GroundTruth struct {
	Target   string      `yaml:"target"`
	Findings []GTFinding `yaml:"findings"`
}

// GTFinding is one expected finding from the ground truth.
type GTFinding struct {
	Type       string `yaml:"type"`
	Severity   string `yaml:"severity"`
	Observable string `yaml:"observable"` // canonical match key
	Required   bool   `yaml:"required"`
	Notes      string `yaml:"notes,omitempty"`
}

// DBFinding is a finding row read from the database.
type DBFinding struct {
	FindingID  string
	Type       string
	Severity   string
	Observable string
	Analysis   string
}

// RunMetrics holds performance metrics collected from the database for one session.
type RunMetrics struct {
	DurationSec  float64
	TotalCostUSD float64
	TokensIn     int
	TokensOut    int
	FindingCount int
	AvgLatencyMs int
}

// FindingScore holds the quality scores for one variant run.
type FindingScore struct {
	Recall    float64
	Precision float64
	F1        float64
	FPRate    float64
	Matched   []MatchedFinding
	Missed    []GTFinding
	FalsePos  []DBFinding
}

// MatchedFinding pairs a GT finding with an agent finding.
type MatchedFinding struct {
	GT      GTFinding
	Found   DBFinding
	Score   float64 // 0.6–1.0
	Fuzzy   bool
}

// VariantResult holds metrics + score for one variant × repeat.
type VariantResult struct {
	VariantID      string
	Description    string
	RepeatNum      int
	SessionID      string
	StartedAt      time.Time
	CompletedAt    time.Time
	Metrics        RunMetrics
	Score          FindingScore
	SystemOverview string
}

// EvalRun aggregates all variant results for one eval invocation.
type EvalRun struct {
	ID          string
	Name        string
	StartedAt   time.Time
	CompletedAt time.Time
	Results     []VariantResult
}

// EvalScore is the flattened DB record for one eval_sessions row with scores.
type EvalScore struct {
	EvalRunID  string
	SessionID  string
	VariantID  string
	RepeatNum  int
	Recall     float64
	Precision  float64
	F1         float64
	FPRate     float64
	ScoreJSON  string
}
