package eval

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadEvalConfig reads and validates an eval configuration file.
func LoadEvalConfig(path string) (*EvalConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read eval config: %w", err)
	}
	var cfg EvalConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse eval config: %w", err)
	}
	if err := validateEvalConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadGroundTruth reads and validates a ground truth file.
func LoadGroundTruth(path string) (*GroundTruth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ground truth: %w", err)
	}
	var gt GroundTruth
	if err := yaml.Unmarshal(data, &gt); err != nil {
		return nil, fmt.Errorf("parse ground truth: %w", err)
	}
	if len(gt.Findings) == 0 {
		return nil, fmt.Errorf("ground truth has no findings")
	}
	return &gt, nil
}

func validateEvalConfig(cfg *EvalConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("eval config: name is required")
	}
	if cfg.GroundTruth == "" {
		return fmt.Errorf("eval config: ground_truth path is required")
	}
	if len(cfg.Variants) == 0 {
		return fmt.Errorf("eval config: at least one variant is required")
	}
	seen := make(map[string]bool)
	for i, v := range cfg.Variants {
		if v.ID == "" {
			return fmt.Errorf("eval config: variant[%d] missing id", i)
		}
		if seen[v.ID] {
			return fmt.Errorf("eval config: duplicate variant id %q", v.ID)
		}
		seen[v.ID] = true
		if len(v.Agents) == 0 {
			return fmt.Errorf("eval config: variant %q has no agents defined", v.ID)
		}
	}
	if cfg.Repeat <= 0 {
		cfg.Repeat = 1
	}
	return nil
}
