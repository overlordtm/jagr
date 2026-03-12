package main

import (
	"flag"
	"fmt"
	"os"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/overlordtm/jagr/internal/gateway"
	"github.com/overlordtm/jagr/internal/gateway/models"
)

func main() {
	configPath := flag.String("config", "gateway.yaml", "Path to gateway config file")
	verbose := flag.Bool("verbose", false, "Verbose logging")

	flag.Parse()

	// Load config
	config, err := loadConfig(*configPath)
	if err != nil {
		fmt.Printf("Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Build logger
	level := zap.InfoLevel
	if *verbose {
		level = zap.DebugLevel
	}

	zapConfig := zap.NewProductionConfig()
	zapConfig.Level.SetLevel(level)
	logger, err := zapConfig.Build()
	if err != nil {
		fmt.Printf("Failed to build logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Info("Gateway starting",
		zap.String("config", *configPath),
		zap.String("listen", config.Server.Listen))

	// Create and start gateway
	gw, err := gateway.NewGateway(config, logger)
	if err != nil {
		logger.Error("Failed to create gateway", zap.Error(err))
		os.Exit(1)
	}

	if err := gw.Start(); err != nil {
		logger.Error("Gateway failed", zap.Error(err))
		os.Exit(1)
	}
}

func loadConfig(path string) (*models.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config models.Config
	if err := yaml.Unmarshal(data, &config); err != nil {
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
