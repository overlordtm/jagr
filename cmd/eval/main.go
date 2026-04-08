package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/overlordtm/jagr/internal/eval"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "jagr-eval",
		Short:        "Jagr evaluation harness — compare model/parameter configurations",
		SilenceUsage: true,
	}
	cmd.AddCommand(runCmd(), reportCmd(), scoreCmd())
	return cmd
}

func runCmd() *cobra.Command {
	var (
		evalFile          string
		gatewayURL        string // unused when spawning subprocess; kept for future
		apiKey            string
		gatewayBin        string
		agentBin          string
		baseGatewayConfig string
		dbPath            string
		target            string
		agentHostname     string
		sessionTimeout    int
		outputDir         string
		jsonOutput        bool
		dryRun            bool
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute an eval run against a target",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := eval.LoadEvalConfig(evalFile)
			if err != nil {
				return err
			}

			// Resolve ground truth path relative to eval file.
			gtPath := cfg.GroundTruth
			if !filepath.IsAbs(gtPath) {
				gtPath = filepath.Join(filepath.Dir(evalFile), gtPath)
			}
			gt, err := eval.LoadGroundTruth(gtPath)
			if err != nil {
				return err
			}

			db, err := eval.OpenDB(dbPath)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer db.Close()

			runnerCfg := eval.RunnerConfig{
				GatewayBin:        gatewayBin,
				AgentBin:          agentBin,
				BaseGatewayConfig: baseGatewayConfig,
				APIKey:            apiKey,
				Target:            target,
				AgentHostname:     agentHostname,
				DBPath:            dbPath,
				SessionTimeout:    time.Duration(sessionTimeout) * time.Minute,
				DryRun:            dryRun,
			}

			runner := eval.NewRunner(runnerCfg, db)

			evalRunID := fmt.Sprintf("eval-%s", uuid.New().String()[:8])
			fmt.Printf("Eval run: %s\n", evalRunID)
			fmt.Printf("Variants: %d × repeat %d\n\n", len(cfg.Variants), cfg.Repeat)

			ctx := context.Background()
			run, err := runner.Run(ctx, cfg, gt, evalRunID)
			if err != nil {
				return fmt.Errorf("run failed: %w", err)
			}

			// Write reports.
			if outputDir != "" {
				if err := os.MkdirAll(outputDir, 0755); err != nil {
					return err
				}
			}

			// Markdown report.
			mdPath := filepath.Join(outputDir, fmt.Sprintf("%s-report.md", evalRunID))
			if outputDir == "" {
				eval.WriteMarkdownReport(os.Stdout, run, gt)
			} else {
				f, err := os.Create(mdPath)
				if err != nil {
					return err
				}
				eval.WriteMarkdownReport(f, run, gt)
				f.Close()
				fmt.Printf("\nReport written to %s\n", mdPath)
			}

			// JSON report.
			if jsonOutput {
				jsonPath := filepath.Join(outputDir, fmt.Sprintf("%s-results.json", evalRunID))
				if outputDir == "" {
					eval.WriteJSONReport(os.Stdout, run)
				} else {
					f, err := os.Create(jsonPath)
					if err != nil {
						return err
					}
					eval.WriteJSONReport(f, run)
					f.Close()
					fmt.Printf("JSON written to %s\n", jsonPath)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&evalFile, "eval", "e", "eval.yaml", "Path to eval config YAML")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Agent API key (matches gateway agent_api_key)")
	cmd.Flags().StringVar(&gatewayBin, "gateway-bin", "jagr-gateway", "Path to jagr-gateway binary")
	cmd.Flags().StringVar(&agentBin, "agent-bin", "jagr-agent", "Path to jagr-agent binary")
	cmd.Flags().StringVar(&baseGatewayConfig, "gateway-config", "gateway.yaml", "Base gateway config YAML to merge variants into")
	cmd.Flags().StringVar(&dbPath, "db", "jagr.db", "Path to gateway SQLite database")
	cmd.Flags().StringVar(&target, "target", "local", "Target mode: local or ssh://user@host")
	cmd.Flags().StringVar(&agentHostname, "hostname", "", "Agent hostname (default: system hostname)")
	cmd.Flags().IntVar(&sessionTimeout, "session-timeout", 30, "Minutes to wait for each agent session to complete")
	cmd.Flags().StringVar(&outputDir, "output", "", "Directory for report files (default: stdout)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Also emit JSON report")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print plan without executing")
	_ = gatewayURL // reserved for future --gateway-url flag to reuse running gateway

	return cmd
}

func reportCmd() *cobra.Command {
	var (
		dbPath     string
		evalRunID  string
		gtFile     string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Regenerate report from a stored eval run",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := eval.OpenDB(dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			results, err := eval.GetEvalResults(db, evalRunID)
			if err != nil {
				return fmt.Errorf("load results: %w", err)
			}

			run := &eval.EvalRun{
				ID:      evalRunID,
				Results: results,
			}

			var gt *eval.GroundTruth
			if gtFile != "" {
				gt, err = eval.LoadGroundTruth(gtFile)
				if err != nil {
					return err
				}
			} else {
				gt = &eval.GroundTruth{}
			}

			if jsonOutput {
				return eval.WriteJSONReport(os.Stdout, run)
			}
			eval.WriteMarkdownReport(os.Stdout, run, gt)
			return nil
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "jagr.db", "Path to gateway SQLite database")
	cmd.Flags().StringVar(&evalRunID, "eval-run", "", "Eval run ID to report on")
	cmd.Flags().StringVar(&gtFile, "ground-truth", "", "Ground truth YAML (optional, for finding labels)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON instead of markdown")
	_ = cmd.MarkFlagRequired("eval-run")

	return cmd
}

func scoreCmd() *cobra.Command {
	var (
		dbPath    string
		evalRunID string
		gtFile    string
	)

	cmd := &cobra.Command{
		Use:   "score",
		Short: "Re-score an existing eval run against a (updated) ground truth",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := eval.OpenDB(dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			gt, err := eval.LoadGroundTruth(gtFile)
			if err != nil {
				return err
			}

			results, err := eval.GetEvalResults(db, evalRunID)
			if err != nil {
				return fmt.Errorf("load results: %w", err)
			}

			for i, r := range results {
				findings, err := eval.GetSessionFindings(db, r.SessionID)
				if err != nil {
					return fmt.Errorf("get findings for session %s: %w", r.SessionID, err)
				}
				newScore := eval.Score(findings, *gt)
				results[i].Score = newScore

				fmt.Printf("[%s] repeat %d — F1=%.3f recall=%.3f precision=%.3f\n",
					r.VariantID, r.RepeatNum, newScore.F1, newScore.Recall, newScore.Precision)
			}

			run := &eval.EvalRun{ID: evalRunID, Results: results}
			eval.WriteMarkdownReport(os.Stdout, run, gt)
			return nil
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "jagr.db", "Path to gateway SQLite database")
	cmd.Flags().StringVar(&evalRunID, "eval-run", "", "Eval run ID to re-score")
	cmd.Flags().StringVar(&gtFile, "ground-truth", "", "Updated ground truth YAML")
	_ = cmd.MarkFlagRequired("eval-run")
	_ = cmd.MarkFlagRequired("ground-truth")

	return cmd
}
