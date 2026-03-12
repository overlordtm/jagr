package main

import (
	"flag"
	"fmt"
	"os"

	"go.uber.org/zap"

	"github.com/overlordtm/jagr/internal/agent"
)

func main() {
	agentCmd := flag.NewFlagSet("agent", flag.ExitOnError)

	gatewayURL := agentCmd.String("gateway-url", "", "Gateway server URL (required)")
	apiKey := agentCmd.String("api-key", "", "API key for gateway auth (or JAGR_API_KEY env)")
	mode := agentCmd.String("mode", "interactive", "Execution mode: batch | interactive")
	maxIterations := agentCmd.Int("max-iterations", 50, "Maximum ReAct loop iterations")
	model := agentCmd.String("model", "default", "Model alias to request from gateway")
	objective := agentCmd.String("objective", "", "Custom objective prompt")
	outputDir := agentCmd.String("output-dir", "./jagr-output", "Directory for reports")
	verbose := agentCmd.Bool("verbose", false, "Verbose local logging")
	hostname := agentCmd.String("hostname", "", "Override hostname detection")

	if err := agentCmd.Parse(os.Args[1:]); err != nil {
		fmt.Printf("Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	// Log level
	level := zap.InfoLevel
	if *verbose {
		level = zap.DebugLevel
	}

	// Build logger
	config := zap.NewProductionConfig()
	config.Level.SetLevel(level)
	logger, err := config.Build()
	if err != nil {
		fmt.Printf("Failed to build logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// Get API key from env if not provided
	if *apiKey == "" {
		*apiKey = os.Getenv("JAGR_API_KEY")
	}

	// Validate required flags
	if *gatewayURL == "" {
		fmt.Println("Error: --gateway-url is required")
		agentCmd.PrintDefaults()
		os.Exit(1)
	}

	if *apiKey == "" {
		fmt.Println("Error: --api-key is required or JAGR_API_KEY environment variable must be set")
		os.Exit(1)
	}

	// Get hostname
	if *hostname == "" {
		if h, err := os.Hostname(); err == nil {
			*hostname = h
		} else {
			*hostname = "unknown"
		}
	}

	logger.Info("Agent starting",
		zap.String("gateway_url", *gatewayURL),
		zap.String("mode", *mode),
		zap.String("model", *model),
		zap.String("hostname", *hostname))

	// Create output directory
	if err := os.MkdirAll(*outputDir, 0700); err != nil {
		logger.Error("Failed to create output directory", zap.Error(err))
		os.Exit(1)
	}

	// Initialize clean room
	cleanRoom, err := agent.NewCleanRoom()
	if err != nil {
		logger.Error("Failed to create clean room", zap.Error(err))
		os.Exit(1)
	}
	defer cleanRoom.Cleanup()

	logger.Info("Clean Room initialized", zap.String("work_dir", cleanRoom.WorkDir))

	// Create agent instance
	ag, err := agent.NewAgent(
		*gatewayURL,
		*apiKey,
		*mode,
		*maxIterations,
		*model,
		*objective,
		*outputDir,
		logger,
		cleanRoom,
	)
	if err != nil {
		logger.Error("Failed to create agent", zap.Error(err))
		os.Exit(1)
	}

	// Run the agent
	if err := ag.Run(); err != nil {
		logger.Error("Agent execution failed", zap.Error(err))
		os.Exit(1)
	}
}
