// Package config holds Preledger's runtime configuration: where the
// assumption ledger lives inside the repo, which port the local review UI
// serves on, and the optional LLM triage endpoint.
//
// Triage is optional by design: capture is heuristic-first and works fully
// offline. When a provider is configured, Preledger calls its
// OpenAI-compatible endpoint directly — no OpenAI key, reachable from CN
// networks.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// DefaultPort is the localhost port the review UI binds to.
	DefaultPort = 7830
	// LedgerDirName is the directory scaffolded in the repo root by
	// "preledger install". The ledger travels with the repo, so it is
	// intentionally not gitignored.
	LedgerDirName = ".preledger"
	// LedgerFileName is the append-only JSONL ledger inside LedgerDirName.
	LedgerFileName = "ledger.jsonl"
)

// Triage is the optional LLM assumption-triage call layered on top of the
// offline heuristic extractor. An empty Provider means triage is off.
type Triage struct {
	Provider string // "deepseek" or "glm"
	APIKey   string
	BaseURL  string // OpenAI-compatible endpoint root
	Model    string
}

// Config is the resolved runtime configuration.
type Config struct {
	// RepoRoot is the repository Preledger operates on. Hooks are started
	// by Claude Code with the project dir as cwd, and "preledger up" is run
	// from the repo root, so the working directory is the right default.
	RepoRoot string
	Port     int
	Triage   Triage
}

// providerPresets maps a provider name to its OpenAI-compatible endpoint and
// the plan's target models (deepseek-v3, glm-4.6).
var providerPresets = map[string]Triage{
	"deepseek": {Provider: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-v3"},
	"glm":      {Provider: "glm", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4.6"},
}

// Load resolves the configuration from environment variables over the plan
// defaults: ledger at <repo>/.preledger/ledger.jsonl, review UI on
// localhost:7830, triage off unless PRELEDGER_TRIAGE_PROVIDER is set.
func Load() (Config, error) {
	root, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("resolve repo root: %w", err)
	}
	cfg := Config{RepoRoot: root, Port: DefaultPort}

	if p := os.Getenv("PRELEDGER_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return Config{}, fmt.Errorf("PRELEDGER_PORT %q is not a number", p)
		}
		cfg.Port = n
	}

	if name := strings.ToLower(strings.TrimSpace(os.Getenv("PRELEDGER_TRIAGE_PROVIDER"))); name != "" {
		preset, ok := providerPresets[name]
		if !ok {
			return Config{}, fmt.Errorf("unknown triage provider %q (want deepseek or glm)", name)
		}
		preset.APIKey = os.Getenv("PRELEDGER_TRIAGE_API_KEY")
		cfg.Triage = preset
	}
	return cfg, nil
}

// LedgerDir returns the ledger directory under the repo root.
func (c Config) LedgerDir() string { return filepath.Join(c.RepoRoot, LedgerDirName) }

// LedgerPath returns the append-only JSONL ledger path.
func (c Config) LedgerPath() string { return filepath.Join(c.LedgerDir(), LedgerFileName) }

// ClaudeSettingsPath returns the project-scope Claude Code settings file that
// "preledger install" wires the PostToolUse hook into.
func (c Config) ClaudeSettingsPath() string {
	return filepath.Join(c.RepoRoot, ".claude", "settings.json")
}
