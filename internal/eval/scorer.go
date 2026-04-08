package eval

import (
	"sort"
	"strings"
)

// Score computes finding quality metrics by matching agent findings against ground truth.
func Score(findings []DBFinding, gt GroundTruth) FindingScore {
	if len(gt.Findings) == 0 {
		return FindingScore{}
	}

	type candidate struct {
		gtIdx  int
		fIdx   int
		score  float64
		fuzzy  bool
	}

	// Build all possible matches between findings and GT items.
	var candidates []candidate
	for fi, f := range findings {
		for gi, g := range gt.Findings {
			s, fuzzy := matchScore(f, g)
			if s > 0 {
				candidates = append(candidates, candidate{gi, fi, s, fuzzy})
			}
		}
	}

	// Greedy best-first matching: highest score first.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	matchedGT := make(map[int]bool)
	matchedFinding := make(map[int]bool)
	var matched []MatchedFinding

	for _, c := range candidates {
		if matchedGT[c.gtIdx] || matchedFinding[c.fIdx] {
			continue
		}
		matchedGT[c.gtIdx] = true
		matchedFinding[c.fIdx] = true
		matched = append(matched, MatchedFinding{
			GT:    gt.Findings[c.gtIdx],
			Found: findings[c.fIdx],
			Score: c.score,
			Fuzzy: c.fuzzy,
		})
	}

	// Collect missed GT findings and false positives.
	var missed []GTFinding
	for i, g := range gt.Findings {
		if !matchedGT[i] {
			missed = append(missed, g)
		}
	}
	var falsePos []DBFinding
	for i, f := range findings {
		if !matchedFinding[i] {
			falsePos = append(falsePos, f)
		}
	}

	recall := float64(len(matched)) / float64(len(gt.Findings))
	var precision float64
	if len(findings) > 0 {
		precision = float64(len(matched)) / float64(len(findings))
	}
	var f1 float64
	if recall+precision > 0 {
		f1 = 2 * recall * precision / (recall + precision)
	}
	var fpRate float64
	if len(findings) > 0 {
		fpRate = float64(len(falsePos)) / float64(len(findings))
	}

	return FindingScore{
		Recall:    recall,
		Precision: precision,
		F1:        f1,
		FPRate:    fpRate,
		Matched:   matched,
		Missed:    missed,
		FalsePos:  falsePos,
	}
}

// matchScore returns the match quality (0 = no match) and whether it was a fuzzy match.
func matchScore(f DBFinding, g GTFinding) (float64, bool) {
	nf := normalizeObservable(f.Observable)
	ng := normalizeObservable(g.Observable)

	severityMatch := strings.EqualFold(f.Severity, g.Severity)

	// Exact observable match.
	if nf == ng {
		if severityMatch {
			return 1.0, false
		}
		return 0.75, false
	}

	// Fuzzy match via token Jaccard.
	sim := tokenJaccard(nf, ng)
	if sim >= 0.75 {
		if severityMatch {
			return 0.8, true
		}
		return 0.6, true
	}

	return 0, false
}

// normalizeObservable lowercases and trims whitespace from an observable string.
func normalizeObservable(s string) string {
	return strings.TrimSpace(strings.ToLower(s))
}

// tokenJaccard computes word-level Jaccard similarity by splitting on common path/name separators.
func tokenJaccard(a, b string) float64 {
	setA := tokenSet(a)
	setB := tokenSet(b)

	if len(setA) == 0 && len(setB) == 0 {
		return 1.0
	}

	intersection := 0
	for t := range setA {
		if setB[t] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// tokenSet splits s on common separators and returns a set of non-empty tokens.
func tokenSet(s string) map[string]bool {
	set := make(map[string]bool)
	// Split on path/identifier separators.
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == '/' || r == '-' || r == '_' || r == '.' || r == ' ' || r == ':'
	}) {
		if part != "" {
			set[part] = true
		}
	}
	return set
}
