package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
		logFormat  string
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

			// CLI flag takes precedence, then environment variable, default is json
			format := logFormat
			if format == "" {
				format = os.Getenv("LOG_FORMAT")
			}
			if format == "" {
				format = "json"
			}

			var zapConfig zap.Config
			if format == "console" {
				zapConfig = zap.NewDevelopmentConfig()
			} else {
				zapConfig = zap.NewProductionConfig()
			}

			zapConfig.Level.SetLevel(level)
			logger, err := zapConfig.Build()
			if err != nil {
				return fmt.Errorf("failed to build logger: %w", err)
			}
			defer logger.Sync()

			logger.Info("Gateway starting",
				zap.String("config", configPath),
				zap.String("addr", config.Server.Listen))

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
	rootCmd.Flags().StringVar(&logFormat, "log-format", "", "Log format (json or console)")

	// --- ingest subcommand ---
	var (
		ingestChunkSize int
	)
	ingestCmd := &cobra.Command{
		Use:   "ingest [path...]",
		Short: "Ingest documents into the LightRAG knowledge base",
		Long: `Load files into LightRAG for RAG retrieval by agents.

Accepts file paths or directories (recursively scanned for .md, .txt, .yaml, .yml, .json, .conf files).
Each file is sent as a separate text document. Large files are split into chunks.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := loadConfig(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			if config.Knowledge == nil {
				return fmt.Errorf("knowledge section not configured in %s", configPath)
			}

			baseURL := strings.TrimRight(config.Knowledge.BaseURL, "/")
			apiKey := config.Knowledge.APIKey

			// Collect files from all paths
			var files []string
			for _, p := range args {
				info, err := os.Stat(p)
				if err != nil {
					return fmt.Errorf("cannot access %s: %w", p, err)
				}
				if info.IsDir() {
					err := filepath.Walk(p, func(path string, fi os.FileInfo, err error) error {
						if err != nil || fi.IsDir() {
							return err
						}
						if isIngestableFile(path) {
							files = append(files, path)
						}
						return nil
					})
					if err != nil {
						return fmt.Errorf("walking %s: %w", p, err)
					}
				} else {
					files = append(files, p)
				}
			}

			if len(files) == 0 {
				fmt.Println("No ingestable files found.")
				return nil
			}

			fmt.Printf("Ingesting %d files into LightRAG...\n", len(files))

			var texts, sources []string
			for _, f := range files {
				data, err := os.ReadFile(f)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", f, err)
					continue
				}
				chunks := chunkContent(string(data), ingestChunkSize)
				for _, chunk := range chunks {
					texts = append(texts, chunk)
					sources = append(sources, f)
				}
			}

			body, _ := json.Marshal(map[string]any{"texts": texts, "file_sources": sources})
			req, err := http.NewRequest(http.MethodPost, baseURL+"/documents/texts", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("building request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+apiKey)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("ingest request failed: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("LightRAG returned %s", resp.Status)
			}

			fmt.Printf("Done. Ingested %d chunks from %d files.\n", len(texts), len(files))
			return nil
		},
	}
	ingestCmd.Flags().IntVar(&ingestChunkSize, "chunk-size", 2000, "Max characters per chunk (0 = no chunking)")
	ingestCmd.Flags().StringVar(&configPath, "config", "gateway.yaml", "Path to gateway config file")

	rootCmd.AddCommand(ingestCmd)

	// --- query subcommand ---
	var (
		queryMode string
		queryTopK int
		queryRefs bool
	)
	queryCmd := &cobra.Command{
		Use:   "query <text>",
		Short: "Run a test query against the LightRAG knowledge base",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := loadConfig(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			if config.Knowledge == nil {
				return fmt.Errorf("knowledge section not configured in %s", configPath)
			}

			baseURL := strings.TrimRight(config.Knowledge.BaseURL, "/")
			apiKey := config.Knowledge.APIKey

			mode := queryMode
			if mode == "" {
				mode = config.Knowledge.Mode
			}
			if mode == "" {
				mode = "mix"
			}

			payload := map[string]any{
				"query":              strings.Join(args, " "),
				"mode":               mode,
				"include_references": queryRefs,
			}
			if queryTopK > 0 {
				payload["top_k"] = queryTopK
			}

			body, _ := json.Marshal(payload)
			req, err := http.NewRequest(http.MethodPost, baseURL+"/query", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("building request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+apiKey)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("query failed: %w", err)
			}
			defer resp.Body.Close()

			var result map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("decoding response (status %s): %w", resp.Status, err)
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("LightRAG returned %s: %v", resp.Status, result)
			}

			fmt.Println(result["response"])

			if queryRefs {
				if refs, ok := result["references"]; ok && refs != nil {
					fmt.Println("\n--- References ---")
					refsJSON, _ := json.MarshalIndent(refs, "", "  ")
					fmt.Println(string(refsJSON))
				}
			}
			return nil
		},
	}
	queryCmd.Flags().StringVar(&queryMode, "mode", "", "Query mode: local, global, hybrid, naive, mix, bypass (default from config)")
	queryCmd.Flags().IntVar(&queryTopK, "top-k", 0, "Number of top entities/relations to retrieve (0 = LightRAG default)")
	queryCmd.Flags().BoolVar(&queryRefs, "refs", false, "Print source references alongside the response")
	queryCmd.Flags().StringVar(&configPath, "config", "gateway.yaml", "Path to gateway config file")

	rootCmd.AddCommand(queryCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var ingestableExts = map[string]bool{
	".md": true, ".txt": true, ".yaml": true, ".yml": true,
	".json": true, ".conf": true, ".cfg": true, ".ini": true,
	".toml": true, ".sh": true, ".py": true, ".go": true,
}

func isIngestableFile(path string) bool {
	return ingestableExts[strings.ToLower(filepath.Ext(path))]
}

// chunkContent splits text into chunks of approximately maxChars, breaking at
// paragraph or line boundaries. Returns the full content as a single chunk if
// maxChars <= 0 or the content is short enough.
func chunkContent(content string, maxChars int) []string {
	if maxChars <= 0 || len(content) <= maxChars {
		return []string{content}
	}

	var chunks []string
	lines := strings.Split(content, "\n")
	var current strings.Builder

	for _, line := range lines {
		if current.Len()+len(line)+1 > maxChars && current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
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
		config.Session.TimeoutMinutes = 30
	}
	if config.DefaultProvider == "" {
		config.DefaultProvider = "openrouter"
	}
	if config.DefaultModel == "" {
		config.DefaultModel = "default"
	}
	if config.Timeouts.ProviderTimeoutSeconds == 0 {
		config.Timeouts.ProviderTimeoutSeconds = 300
	}
	if config.Timeouts.PricingFetchTimeoutSeconds == 0 {
		config.Timeouts.PricingFetchTimeoutSeconds = 30
	}

	return &config, nil
}
