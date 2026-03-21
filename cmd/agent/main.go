package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/overlordtm/jagr/internal/agent"
)

func main() {
	var (
		gatewayURL    string
		apiKey        string
		mode          string
		maxIterations int
		maxTokens     int
		model         string
		objective     string
		outputDir     string
		verbose       bool
		hostname      string
	)

	rootCmd := &cobra.Command{
		Use:   "jagr-agent",
		Short: "JAGR security assessment agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Build logger
			level := zap.InfoLevel
			if verbose {
				level = zap.DebugLevel
			}

			config := zap.NewProductionConfig()
			config.Level.SetLevel(level)
			logger, err := config.Build()
			if err != nil {
				return fmt.Errorf("failed to build logger: %w", err)
			}
			defer logger.Sync()

			// Validate required flags
			if gatewayURL == "" {
				return fmt.Errorf("--gateway-url is required (or set JAGR_GATEWAY_URL)")
			}
			if apiKey == "" {
				return fmt.Errorf("--api-key is required (or set JAGR_API_KEY)")
			}

			// Get hostname
			if hostname == "" {
				if h, err := os.Hostname(); err == nil {
					hostname = h
				} else {
					hostname = "unknown"
				}
			}

			logger.Info("Agent starting",
				zap.String("gateway_url", gatewayURL),
				zap.String("mode", mode),
				zap.String("model", model),
				zap.String("hostname", hostname))

			// Create output directory
			if err := os.MkdirAll(outputDir, 0700); err != nil {
				return fmt.Errorf("failed to create output directory: %w", err)
			}

			// Initialize clean room
			cleanRoom, err := agent.NewCleanRoom()
			if err != nil {
				return fmt.Errorf("failed to create clean room: %w", err)
			}
			defer cleanRoom.Cleanup()

			logger.Info("Clean Room initialized", zap.String("work_dir", cleanRoom.WorkDir))

			// Create agent instance
			ag, err := agent.NewAgent(
				gatewayURL,
				apiKey,
				mode,
				maxIterations,
				maxTokens,
				model,
				objective,
				outputDir,
				logger,
				cleanRoom,
			)
			if err != nil {
				return fmt.Errorf("failed to create agent: %w", err)
			}

			return ag.Run()
		},
	}

	flags := rootCmd.Flags()
	flags.StringVar(&gatewayURL, "gateway-url", "", "Gateway server URL (required)")
	flags.StringVar(&apiKey, "api-key", "", "API key for gateway auth")
	flags.StringVar(&mode, "mode", "interactive", "Execution mode: batch | interactive")
	flags.IntVar(&maxIterations, "max-iterations", 50, "Maximum ReAct loop iterations")
	flags.IntVar(&maxTokens, "max-tokens", 500000, "Maximum total tokens consumed before concluding")
	flags.StringVar(&model, "model", "default", "Model alias to request from gateway")
	flags.StringVar(&objective, "objective", "", "Custom objective prompt")
	flags.StringVar(&outputDir, "output-dir", "./jagr-output", "Directory for reports")
	flags.BoolVar(&verbose, "verbose", false, "Verbose local logging")
	flags.StringVar(&hostname, "hostname", "", "Override hostname detection")

	// Bind env vars
	rootCmd.PreRun = func(cmd *cobra.Command, args []string) {
		if !cmd.Flags().Changed("gateway-url") {
			if v := os.Getenv("JAGR_GATEWAY_URL"); v != "" {
				gatewayURL = v
			}
		}
		if !cmd.Flags().Changed("api-key") {
			if v := os.Getenv("JAGR_API_KEY"); v != "" {
				apiKey = v
			}
		}
	}

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
