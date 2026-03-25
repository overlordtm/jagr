package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/overlordtm/jagr/internal/agent"
)

func main() {
	var (
		gatewayURL            string
		apiKey                string
		mode                  string
		maxIterations         int
		model                 string
		objective             string
		outputDir             string
		verbose               bool
		hostname              string
		tlsSkipVerify         bool
		maxToolFailures       int
		httpTimeoutSec        int
		commandTimeoutSec     int
		longCommandTimeoutSec int
		remoteHost            string
	)

	rootCmd := &cobra.Command{
		Use:          "jagr-agent",
		Short:        "JAGR security assessment agent",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Build logger — human-readable console format for agent
			level := zap.InfoLevel
			if verbose {
				level = zap.DebugLevel
			}

			config := zap.NewDevelopmentConfig()
			config.Level.SetLevel(level)
			config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
			config.DisableStacktrace = true
			logger, err := config.Build()
			if err != nil {
				return fmt.Errorf("failed to build logger: %w", err)
			}
			defer logger.Sync()

			// Remote execution: copy self to remote host and run there
			if remoteHost != "" {
				remoteArgs := buildRemoteArgs(cmd)
				logger.Info("Remote mode enabled",
					zap.String("remote_host", remoteHost),
					zap.Strings("remote_args", remoteArgs))
				return agent.RemoteExec(logger, remoteHost, gatewayURL, outputDir, remoteArgs)
			}

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

			// Store artifacts under a hostname-specific subdirectory
			outputDir = filepath.Join(outputDir, hostname)

			logger.Info("Agent starting",
				zap.String("gateway_url", gatewayURL),
				zap.String("mode", mode),
				zap.String("model", model),
				zap.String("hostname", hostname),
				zap.String("output_dir", outputDir))

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

			// Apply configurable timeouts to clean room
			if commandTimeoutSec > 0 {
				cleanRoom.DefaultTimeout = time.Duration(commandTimeoutSec) * time.Second
			}
			if longCommandTimeoutSec > 0 {
				cleanRoom.LongTimeout = time.Duration(longCommandTimeoutSec) * time.Second
			}

			logger.Info("Clean Room initialized", zap.String("work_dir", cleanRoom.WorkDir))

			// Create gateway client
			gw := agent.NewGatewayClient(
				gatewayURL,
				apiKey,
				hostname,
				model,
				logger,
				tlsSkipVerify,
				time.Duration(httpTimeoutSec)*time.Second,
			)

			// Create harness
			harness := agent.NewJagrHarness(
				mode,
				maxIterations,
				maxToolFailures,
				model,
				objective,
				outputDir,
				logger,
				gw,
				cleanRoom,
			)

			return harness.Run()
		},
	}

	flags := rootCmd.Flags()
	flags.StringVar(&gatewayURL, "gateway-url", "", "Gateway server URL (required) (env: JAGR_GATEWAY_URL)")
	flags.StringVar(&apiKey, "api-key", "", "API key for gateway auth (env: JAGR_API_KEY)")
	flags.StringVar(&mode, "mode", "interactive", "Execution mode: batch | interactive (env: JAGR_MODE)")
	flags.IntVar(&maxIterations, "max-iterations", 50, "Maximum ReAct loop iterations (env: JAGR_MAX_ITERATIONS)")
	flags.IntVar(&maxToolFailures, "max-tool-failures", 5, "Max consecutive failures per tool before circuit breaker trips (env: JAGR_MAX_TOOL_FAILURES)")
	flags.StringVar(&model, "model", "default", "Model alias to request from gateway (env: JAGR_MODEL)")
	flags.StringVar(&objective, "objective", "", "Custom objective prompt (env: JAGR_OBJECTIVE)")
	flags.StringVar(&outputDir, "output-dir", "./jagr-output", "Directory for reports (env: JAGR_OUTPUT_DIR)")
	flags.BoolVar(&verbose, "verbose", false, "Verbose local logging (env: JAGR_VERBOSE)")
	flags.StringVar(&hostname, "hostname", "", "Override hostname detection (env: JAGR_HOSTNAME)")
	flags.BoolVar(&tlsSkipVerify, "tls-skip-verify", false, "Skip TLS certificate verification (trust self-signed certs) (env: JAGR_TLS_SKIP_VERIFY)")
	flags.IntVar(&httpTimeoutSec, "http-timeout", 120, "HTTP request timeout in seconds for gateway communication (env: JAGR_HTTP_TIMEOUT)")
	flags.IntVar(&commandTimeoutSec, "command-timeout", 300, "Default command execution timeout in seconds (env: JAGR_COMMAND_TIMEOUT)")
	flags.IntVar(&longCommandTimeoutSec, "long-command-timeout", 900, "Long-running command execution timeout in seconds (env: JAGR_LONG_COMMAND_TIMEOUT)")
	flags.StringVar(&remoteHost, "remote", "", "Copy agent to remote host via SSH and execute there (env: JAGR_REMOTE)")

	// Bind env vars
	rootCmd.PreRun = func(cmd *cobra.Command, args []string) {
		bindStringEnv(cmd, "gateway-url", "JAGR_GATEWAY_URL", &gatewayURL)
		bindStringEnv(cmd, "api-key", "JAGR_API_KEY", &apiKey)
		bindStringEnv(cmd, "mode", "JAGR_MODE", &mode)
		bindIntEnv(cmd, "max-iterations", "JAGR_MAX_ITERATIONS", &maxIterations)
		bindIntEnv(cmd, "max-tool-failures", "JAGR_MAX_TOOL_FAILURES", &maxToolFailures)
		bindStringEnv(cmd, "model", "JAGR_MODEL", &model)
		bindStringEnv(cmd, "objective", "JAGR_OBJECTIVE", &objective)
		bindStringEnv(cmd, "output-dir", "JAGR_OUTPUT_DIR", &outputDir)
		bindBoolEnv(cmd, "verbose", "JAGR_VERBOSE", &verbose)
		bindStringEnv(cmd, "hostname", "JAGR_HOSTNAME", &hostname)
		bindBoolEnv(cmd, "tls-skip-verify", "JAGR_TLS_SKIP_VERIFY", &tlsSkipVerify)
		bindIntEnv(cmd, "http-timeout", "JAGR_HTTP_TIMEOUT", &httpTimeoutSec)
		bindIntEnv(cmd, "command-timeout", "JAGR_COMMAND_TIMEOUT", &commandTimeoutSec)
		bindIntEnv(cmd, "long-command-timeout", "JAGR_LONG_COMMAND_TIMEOUT", &longCommandTimeoutSec)
		bindStringEnv(cmd, "remote", "JAGR_REMOTE", &remoteHost)
	}

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// buildRemoteArgs reconstructs CLI flags from the current command, excluding --remote.
func buildRemoteArgs(cmd *cobra.Command) []string {
	var args []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if f.Name == "remote" {
			return
		}
		args = append(args, fmt.Sprintf("--%s=%s", f.Name, f.Value.String()))
	})
	return args
}

func bindStringEnv(cmd *cobra.Command, flagName, envName string, target *string) {
	if !cmd.Flags().Changed(flagName) {
		if v := os.Getenv(envName); v != "" {
			*target = v
		}
	}
}

func bindIntEnv(cmd *cobra.Command, flagName, envName string, target *int) {
	if !cmd.Flags().Changed(flagName) {
		if v := os.Getenv(envName); v != "" {
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				*target = n
			} else {
				fmt.Fprintf(os.Stderr, "warning: invalid %s=%q, using default\n", envName, v)
			}
		}
	}
}

func bindBoolEnv(cmd *cobra.Command, flagName, envName string, target *bool) {
	if !cmd.Flags().Changed(flagName) {
		if v := os.Getenv(envName); v != "" {
			*target = (v == "true" || v == "1" || v == "yes")
		}
	}
}
