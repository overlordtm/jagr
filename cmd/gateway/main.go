package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/overlordtm/jagr/internal/gateway"
	"github.com/overlordtm/jagr/internal/gateway/models"
)

func main() {
	var (
		configPath string
		verbose    bool
	)

	rootCmd := &cobra.Command{
		Use:   "jagr-gateway",
		Short: "JAGR gateway server",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			config, err := loadConfig(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Build logger
			level := zap.InfoLevel
			if verbose {
				level = zap.DebugLevel
			}

			zapConfig := zap.NewProductionConfig()
			zapConfig.Level.SetLevel(level)
			logger, err := zapConfig.Build()
			if err != nil {
				return fmt.Errorf("failed to build logger: %w", err)
			}
			defer logger.Sync()

			logger.Info("Gateway starting",
				zap.String("config", configPath),
				zap.String("listen", config.Server.Listen))

			// Create and start gateway
			gw, err := gateway.NewGateway(config, logger)
			if err != nil {
				return fmt.Errorf("failed to create gateway: %w", err)
			}

			return gw.Start()
		},
	}

	rootCmd.Flags().StringVar(&configPath, "config", "gateway.yaml", "Path to gateway config file")
	rootCmd.Flags().BoolVar(&verbose, "verbose", false, "Verbose logging")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func loadConfig(path string) (*models.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Expand environment variables (e.g., ${OPENROUTER_API_KEY})
	expanded := os.ExpandEnv(string(data))

	var config models.Config
	if err := yaml.Unmarshal([]byte(expanded), &config); err != nil {
		return nil, err
	}

	// Set defaults
	if config.Server.Listen == "" {
		config.Server.Listen = ":8443"
	}
	if config.Database.Path == "" {
		config.Database.Path = "/var/lib/jagr/jagr.db"
	}
	if config.RateLimit.RequestsPerMinute == 0 {
		config.RateLimit.RequestsPerMinute = 30
	}
	if config.RateLimit.MaxConcurrent == 0 {
		config.RateLimit.MaxConcurrent = 5
	}
	if config.Session.TimeoutMinutes == 0 {
		config.Session.TimeoutMinutes = 120
	}
	if config.Session.HistoryMode == "" {
		config.Session.HistoryMode = "gateway"
	}
	if config.DefaultProvider == "" {
		config.DefaultProvider = "openrouter"
	}
	if config.DefaultModel == "" {
		config.DefaultModel = "default"
	}

	return &config, nil
}
