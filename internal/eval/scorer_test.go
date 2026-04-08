package eval

import (
	"math"
	"testing"
)

func TestNormalizeObservable(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"  /etc/cron.d/Backdoor  ", "/etc/cron.d/backdoor"},
		{"BackdoorUser", "backdooruser"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeObservable(c.in); got != c.want {
			t.Errorf("normalizeObservable(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTokenJaccard(t *testing.T) {
	cases := []struct {
		a, b string
		min  float64
	}{
		{"/etc/cron.d/backdoor", "/etc/cron.d/backdoor", 1.0},
		{"/etc/cron.d/cleanup", "/etc/cron.d/backdoor", 0.4},  // shares etc/cron.d, not last token
		{"backdoor_user", "backdoor-user", 1.0},               // same tokens different sep
		{"suid_wrapper", "completely_unrelated", 0.0},
	}
	for _, c := range cases {
		got := tokenJaccard(c.a, c.b)
		if got < c.min-0.01 {
			t.Errorf("tokenJaccard(%q, %q) = %.2f, want >= %.2f", c.a, c.b, got, c.min)
		}
	}
}

func TestScore_ExactMatch(t *testing.T) {
	gt := GroundTruth{
		Target: "host",
		Findings: []GTFinding{
			{Type: "persistence", Severity: "critical", Observable: "/etc/cron.d/backdoor", Required: true},
			{Type: "privilege_escalation", Severity: "high", Observable: "/usr/local/bin/suid", Required: true},
		},
	}
	findings := []DBFinding{
		{FindingID: "1", Type: "persistence", Severity: "critical", Observable: "/etc/cron.d/backdoor"},
		{FindingID: "2", Type: "privilege_escalation", Severity: "high", Observable: "/usr/local/bin/suid"},
	}

	s := Score(findings, gt)

	if s.Recall != 1.0 {
		t.Errorf("recall = %.2f, want 1.0", s.Recall)
	}
	if s.Precision != 1.0 {
		t.Errorf("precision = %.2f, want 1.0", s.Precision)
	}
	if s.F1 != 1.0 {
		t.Errorf("f1 = %.2f, want 1.0", s.F1)
	}
	if s.FPRate != 0.0 {
		t.Errorf("fp_rate = %.2f, want 0.0", s.FPRate)
	}
	if len(s.Missed) != 0 {
		t.Errorf("missed = %d, want 0", len(s.Missed))
	}
	if len(s.Matched) != 2 {
		t.Errorf("matched = %d, want 2", len(s.Matched))
	}
	for _, m := range s.Matched {
		if m.Fuzzy {
			t.Error("expected exact match, got fuzzy")
		}
		if m.Score != 1.0 {
			t.Errorf("match score = %.2f, want 1.0", m.Score)
		}
	}
}

func TestScore_SeverityMismatch(t *testing.T) {
	gt := GroundTruth{
		Findings: []GTFinding{
			{Type: "persistence", Severity: "critical", Observable: "/etc/cron.d/backdoor"},
		},
	}
	findings := []DBFinding{
		{FindingID: "1", Type: "persistence", Severity: "low", Observable: "/etc/cron.d/backdoor"},
	}

	s := Score(findings, gt)

	if len(s.Matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(s.Matched))
	}
	if s.Matched[0].Score != 0.75 {
		t.Errorf("score = %.2f, want 0.75 for severity mismatch", s.Matched[0].Score)
	}
}

func TestScore_FuzzyMatch(t *testing.T) {
	gt := GroundTruth{
		Findings: []GTFinding{
			{Type: "persistence", Severity: "high", Observable: "/etc/cron.d/evil-cleanup"},
		},
	}
	// Observable shares 3 of 4 tokens with GT
	findings := []DBFinding{
		{FindingID: "1", Type: "persistence", Severity: "high", Observable: "/etc/cron.d/cleanup"},
	}

	s := Score(findings, gt)

	if len(s.Matched) == 0 {
		// Jaccard of {etc,cron,d,cleanup} vs {etc,cron,d,evil,cleanup} = 4/5 = 0.8 >= 0.75
		// But "evil-cleanup" splits to {evil, cleanup} not just {cleanup}...
		// Actually: a={etc,cron,d,cleanup}, b={etc,cron,d,evil,cleanup}
		// intersection={etc,cron,d,cleanup}=4, union=5 → 0.8
		t.Errorf("expected fuzzy match, got none (missed=%v, fp=%v)", s.Missed, s.FalsePos)
	} else if !s.Matched[0].Fuzzy {
		t.Error("expected fuzzy=true")
	}
}

func TestScore_MissedAndFalsePositive(t *testing.T) {
	gt := GroundTruth{
		Findings: []GTFinding{
			{Type: "persistence", Severity: "critical", Observable: "/etc/cron.d/backdoor", Required: true},
			{Type: "user_account", Severity: "medium", Observable: "backdoor_user"},
		},
	}
	// Agent finds one correct + one false positive, misses one GT item
	findings := []DBFinding{
		{FindingID: "1", Type: "persistence", Severity: "critical", Observable: "/etc/cron.d/backdoor"},
		{FindingID: "2", Type: "network", Severity: "low", Observable: "some_unrelated_port"},
	}

	s := Score(findings, gt)

	if len(s.Matched) != 1 {
		t.Errorf("matched = %d, want 1", len(s.Matched))
	}
	if len(s.Missed) != 1 {
		t.Errorf("missed = %d, want 1", len(s.Missed))
	}
	if len(s.FalsePos) != 1 {
		t.Errorf("false_pos = %d, want 1", len(s.FalsePos))
	}

	wantRecall := 0.5
	if math.Abs(s.Recall-wantRecall) > 0.01 {
		t.Errorf("recall = %.2f, want %.2f", s.Recall, wantRecall)
	}
	wantPrecision := 0.5
	if math.Abs(s.Precision-wantPrecision) > 0.01 {
		t.Errorf("precision = %.2f, want %.2f", s.Precision, wantPrecision)
	}
}

func TestScore_EmptyGroundTruth(t *testing.T) {
	gt := GroundTruth{Findings: nil}
	findings := []DBFinding{
		{FindingID: "1", Observable: "something"},
	}
	s := Score(findings, gt)
	// All zeros when there's no ground truth
	if s.F1 != 0 || s.Recall != 0 || s.Precision != 0 {
		t.Errorf("expected zero scores for empty GT, got %+v", s)
	}
}

func TestScore_NoFindings(t *testing.T) {
	gt := GroundTruth{
		Findings: []GTFinding{
			{Type: "persistence", Severity: "critical", Observable: "/etc/cron.d/backdoor"},
		},
	}
	s := Score(nil, gt)

	if s.Recall != 0 {
		t.Errorf("recall = %.2f, want 0.0", s.Recall)
	}
	if s.Precision != 0 {
		t.Errorf("precision = %.2f, want 0.0", s.Precision)
	}
	if len(s.Missed) != 1 {
		t.Errorf("missed = %d, want 1", len(s.Missed))
	}
}
