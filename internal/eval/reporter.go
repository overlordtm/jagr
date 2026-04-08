package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// WriteMarkdownReport writes a human-readable comparison table to w.
func WriteMarkdownReport(w io.Writer, run *EvalRun, gt *GroundTruth) {
	fmt.Fprintf(w, "# Eval: %s\n\n", run.Name)
	fmt.Fprintf(w, "**Date:** %s  \n", run.StartedAt.Format(time.RFC3339))
	if gt != nil && gt.Target != "" {
		fmt.Fprintf(w, "**Target:** %s  \n", gt.Target)
	}
	fmt.Fprintf(w, "**Ground truth findings:** %d  \n\n", len(gt.Findings))

	// Aggregate results by variant (average over repeats).
	type variantSummary struct {
		id          string
		description string
		results     []VariantResult
	}
	variantMap := make(map[string]*variantSummary)
	variantOrder := []string{}
	for _, r := range run.Results {
		if _, ok := variantMap[r.VariantID]; !ok {
			variantMap[r.VariantID] = &variantSummary{id: r.VariantID, description: r.Description}
			variantOrder = append(variantOrder, r.VariantID)
		}
		variantMap[r.VariantID].results = append(variantMap[r.VariantID].results, r)
	}

	type avgMetrics struct {
		recall, precision, f1, fpRate float64
		costUSD                        float64
		tokensIn, tokensOut            float64
		durationSec                    float64
		findingCount                   float64
	}

	avgs := make(map[string]avgMetrics)
	for _, id := range variantOrder {
		vs := variantMap[id]
		var a avgMetrics
		n := float64(len(vs.results))
		for _, r := range vs.results {
			a.recall += r.Score.Recall
			a.precision += r.Score.Precision
			a.f1 += r.Score.F1
			a.fpRate += r.Score.FPRate
			a.costUSD += r.Metrics.TotalCostUSD
			a.tokensIn += float64(r.Metrics.TokensIn)
			a.tokensOut += float64(r.Metrics.TokensOut)
			a.durationSec += r.Metrics.DurationSec
			a.findingCount += float64(r.Metrics.FindingCount)
		}
		if n > 0 {
			a.recall /= n
			a.precision /= n
			a.f1 /= n
			a.fpRate /= n
			a.costUSD /= n
			a.tokensIn /= n
			a.tokensOut /= n
			a.durationSec /= n
			a.findingCount /= n
		}
		avgs[id] = a
	}

	// Header row.
	header := "| Metric |"
	sep := "|--------|"
	for _, id := range variantOrder {
		label := id
		if variantMap[id].description != "" {
			label = id
		}
		n := len(variantMap[id].results)
		suffix := ""
		if n > 1 {
			suffix = fmt.Sprintf(" (n=%d)", n)
		}
		col := fmt.Sprintf(" %s%s |", label, suffix)
		header += col
		sep += strings.Repeat("-", len(col)-2) + "|"
	}
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, sep)

	// Metric rows.
	rows := []struct {
		label  string
		format func(id string) string
	}{
		{"Recall", func(id string) string { return fmt.Sprintf("%.3f", avgs[id].recall) }},
		{"Precision", func(id string) string { return fmt.Sprintf("%.3f", avgs[id].precision) }},
		{"F1", func(id string) string { return fmt.Sprintf("%.3f", avgs[id].f1) }},
		{"False Positive Rate", func(id string) string { return fmt.Sprintf("%.3f", avgs[id].fpRate) }},
		{"Findings (avg)", func(id string) string { return fmt.Sprintf("%.1f", avgs[id].findingCount) }},
		{"Cost USD (avg)", func(id string) string { return fmt.Sprintf("$%.4f", avgs[id].costUSD) }},
		{"Tokens In (avg)", func(id string) string { return fmt.Sprintf("%d", int(avgs[id].tokensIn)) }},
		{"Tokens Out (avg)", func(id string) string { return fmt.Sprintf("%d", int(avgs[id].tokensOut)) }},
		{"Duration s (avg)", func(id string) string { return fmt.Sprintf("%.0f", avgs[id].durationSec) }},
	}

	for _, row := range rows {
		line := fmt.Sprintf("| %s |", row.label)
		for _, id := range variantOrder {
			line += fmt.Sprintf(" %s |", row.format(id))
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w)

	// Per-variant detail sections.
	for _, id := range variantOrder {
		vs := variantMap[id]
		fmt.Fprintf(w, "## Variant: %s\n", id)
		if vs.description != "" {
			fmt.Fprintf(w, "> %s\n\n", vs.description)
		}

		for _, r := range vs.results {
			repeatLabel := ""
			if len(vs.results) > 1 {
				repeatLabel = fmt.Sprintf(" (repeat %d)", r.RepeatNum)
			}
			fmt.Fprintf(w, "### Run%s — session `%s`\n\n", repeatLabel, r.SessionID)

			if r.SystemOverview != "" {
				fmt.Fprintf(w, "**System Overview:**\n\n%s\n\n", r.SystemOverview)
			}

			if len(r.Score.Missed) > 0 {
				fmt.Fprintf(w, "**Missed findings (%d):**\n\n", len(r.Score.Missed))
				for _, g := range r.Score.Missed {
					req := ""
					if g.Required {
						req = " ⚠️ required"
					}
					fmt.Fprintf(w, "- `%s` [%s/%s]%s\n", g.Observable, g.Type, g.Severity, req)
				}
				fmt.Fprintln(w)
			}

			if len(r.Score.FalsePos) > 0 {
				fmt.Fprintf(w, "**False positives (%d):**\n\n", len(r.Score.FalsePos))
				for _, f := range r.Score.FalsePos {
					fmt.Fprintf(w, "- `%s` [%s/%s]\n", f.Observable, f.Type, f.Severity)
				}
				fmt.Fprintln(w)
			}

			if len(r.Score.Matched) > 0 {
				fmt.Fprintf(w, "**Matched findings (%d):**\n\n", len(r.Score.Matched))
				for _, m := range r.Score.Matched {
					fuzzyTag := ""
					if m.Fuzzy {
						fuzzyTag = " (fuzzy)"
					}
					fmt.Fprintf(w, "- `%s` → score=%.2f%s\n", m.Found.Observable, m.Score, fuzzyTag)
				}
				fmt.Fprintln(w)
			}
		}
	}
}

// WriteJSONReport writes a machine-readable JSON report.
func WriteJSONReport(w io.Writer, run *EvalRun) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(run)
}
