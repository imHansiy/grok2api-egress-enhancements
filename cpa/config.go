package main

// config.go loads and hot-reloads the CPA quality-guard policy. The policy
// mirrors grok2api egress quality-guard knobs: detection mode, intervals,
// thresholds, consecutive counts, quarantine duration, and minimum healthy
// node protection.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// detectionMode selects the passive/active detection strategy.
type detectionMode string

const (
	modePassive detectionMode = "passive" // passive audit only, no active re-probe
	modeActive  detectionMode = "active"  // passive audit + active re-probe on soft hits
)

// pluginConfig is the plugin-owned configuration.
type pluginConfig struct {
	// Enabled toggles the whole quality guard.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Mode selects detection behavior.
	Mode detectionMode `yaml:"mode" json:"mode"`

	// PassiveOnly when true skips active re-probing entirely.
	PassiveOnly bool `yaml:"passive_only" json:"passive_only"`

	// HardTPS is the passive hard threshold in output tokens/second.
	// Observations at or above this value quarantine immediately.
	HardTPS float64 `yaml:"hard_tps" json:"hard_tps"`

	// SoftTPS is the passive soft threshold. Observations below this value
	// mark the credential suspect and trigger active re-probe (in active mode).
	SoftTPS float64 `yaml:"soft_tps" json:"soft_tps"`

	// SoftHitsBeforeQuarantine is the number of consecutive soft hits after a
	// failed active re-probe before quarantine.
	SoftHitsBeforeQuarantine int `yaml:"soft_hits_before_quarantine" json:"soft_hits_before_quarantine"`

	// MaxConsecutiveErrors is the number of consecutive request/probe errors
	// that force quarantine (0 disables).
	MaxConsecutiveErrors int `yaml:"max_consecutive_errors" json:"max_consecutive_errors"`

	// QuarantineDuration is how long a credential stays quarantined before it
	// becomes eligible for active re-probe.
	QuarantineDuration time.Duration `yaml:"quarantine_duration" json:"quarantine_duration"`

	// RecoveryDelay is the extra delay before a healthy re-probe lifts quarantine.
	RecoveryDelay time.Duration `yaml:"recovery_delay" json:"recovery_delay"`

	// MinHealthy is the minimum number of healthy credentials that must remain.
	// When fewer healthy credentials remain, quarantine is not applied to
	// protect service availability.
	MinHealthy int `yaml:"min_healthy" json:"min_healthy"`

	// ProbeModel is the model used for active re-probes.
	ProbeModel string `yaml:"probe_model" json:"probe_model"`

	// ProbePrompt is the fixed prompt used for active re-probes.
	ProbePrompt string `yaml:"probe_prompt" json:"probe_prompt"`

	// ProbeExpected is the marker that must appear in the re-probe output.
	ProbeExpected string `yaml:"probe_expected" json:"probe_expected"`

	// ProbeMaxOutputTokens bounds the re-probe response size.
	ProbeMaxOutputTokens int `yaml:"probe_max_output_tokens" json:"probe_max_output_tokens"`

	// ProbeHardTPS / ProbeSoftTPS are the active re-probe thresholds.
	ProbeHardTPS float64 `yaml:"probe_hard_tps" json:"probe_hard_tps"`
	ProbeSoftTPS float64 `yaml:"probe_soft_tps" json:"probe_soft_tps"`

	// ProbeTimeout bounds a single active re-probe.
	ProbeTimeout time.Duration `yaml:"probe_timeout" json:"probe_timeout"`

	// OverrideTTL is how long a one-shot scheduler override lives.
	OverrideTTL time.Duration `yaml:"override_ttl" json:"override_ttl"`

	// MonitorInterval is the passive audit accumulation window (informational).
	MonitorInterval time.Duration `yaml:"monitor_interval" json:"monitor_interval"`

	// IncludeReasoningTokens mirrors the panel: output tokens include reasoning.
	IncludeReasoningTokens bool `yaml:"include_reasoning_tokens" json:"include_reasoning_tokens"`
}

// defaultConfig returns the recommended default policy.
func defaultConfig() pluginConfig {
	return pluginConfig{
		Enabled:                  true,
		Mode:                     modeActive,
		PassiveOnly:              false,
		HardTPS:                  1000,
		SoftTPS:                  500,
		SoftHitsBeforeQuarantine: 2,
		MaxConsecutiveErrors:     5,
		QuarantineDuration:       30 * time.Minute,
		RecoveryDelay:            2 * time.Minute,
		MinHealthy:               3,
		ProbeModel:               "grok-3",
		ProbePrompt:              "Reply with the exact marker QUALITY_OK and nothing else.",
		ProbeExpected:            "QUALITY_OK",
		ProbeMaxOutputTokens:     64,
		ProbeHardTPS:             1000,
		ProbeSoftTPS:             500,
		ProbeTimeout:             60 * time.Second,
		OverrideTTL:              30 * time.Second,
		MonitorInterval:          10 * time.Second,
		IncludeReasoningTokens:   true,
	}
}

type configState struct {
	cfg atomic.Value // holds pluginConfig
}

func newConfigState() *configState {
	cs := &configState{}
	cs.cfg.Store(defaultConfig())
	return cs
}

func (cs *configState) get() pluginConfig {
	return cs.cfg.Load().(pluginConfig)
}

func (cs *configState) set(cfg pluginConfig) {
	cs.cfg.Store(cfg)
}

// normalize fills zero-valued fields with defaults and clamps invalid ranges.
func normalizeConfig(cfg pluginConfig) pluginConfig {
	def := defaultConfig()
	if cfg.HardTPS <= 0 {
		cfg.HardTPS = def.HardTPS
	}
	if cfg.SoftTPS <= 0 {
		cfg.SoftTPS = def.SoftTPS
	}
	if cfg.SoftTPS > cfg.HardTPS {
		cfg.SoftTPS = cfg.HardTPS
	}
	if cfg.SoftHitsBeforeQuarantine <= 0 {
		cfg.SoftHitsBeforeQuarantine = def.SoftHitsBeforeQuarantine
	}
	if cfg.MaxConsecutiveErrors < 0 {
		cfg.MaxConsecutiveErrors = def.MaxConsecutiveErrors
	}
	if cfg.QuarantineDuration <= 0 {
		cfg.QuarantineDuration = def.QuarantineDuration
	}
	if cfg.RecoveryDelay < 0 {
		cfg.RecoveryDelay = def.RecoveryDelay
	}
	if cfg.MinHealthy < 0 {
		cfg.MinHealthy = def.MinHealthy
	}
	if strings.TrimSpace(cfg.ProbeModel) == "" {
		cfg.ProbeModel = def.ProbeModel
	}
	if strings.TrimSpace(cfg.ProbePrompt) == "" {
		cfg.ProbePrompt = def.ProbePrompt
	}
	if strings.TrimSpace(cfg.ProbeExpected) == "" {
		cfg.ProbeExpected = def.ProbeExpected
	}
	if cfg.ProbeMaxOutputTokens <= 0 {
		cfg.ProbeMaxOutputTokens = def.ProbeMaxOutputTokens
	}
	if cfg.ProbeHardTPS <= 0 {
		cfg.ProbeHardTPS = def.ProbeHardTPS
	}
	if cfg.ProbeSoftTPS <= 0 {
		cfg.ProbeSoftTPS = def.ProbeSoftTPS
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = def.ProbeTimeout
	}
	if cfg.OverrideTTL <= 0 {
		cfg.OverrideTTL = def.OverrideTTL
	}
	if cfg.MonitorInterval <= 0 {
		cfg.MonitorInterval = def.MonitorInterval
	}
	if cfg.Mode == "" {
		cfg.Mode = def.Mode
	}
	return cfg
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

// handleConfigure processes plugin.register / plugin.reconfigure and returns
// the registration envelope.
func handleConfigure(raw []byte) ([]byte, error) {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode lifecycle request: %w", errUnmarshal)
		}
	}
	cfg := defaultConfig()
	if len(req.ConfigYAML) > 0 {
		decoded, errDecode := decodeConfig(req.ConfigYAML)
		if errDecode != nil {
			return nil, fmt.Errorf("decode config yaml: %w", errDecode)
		}
		cfg = normalizeConfig(decoded)
	}
	globalCfg.set(cfg)
	return okEnvelope(pluginRegistration())
}

func decodeConfig(raw []byte) (pluginConfig, error) {
	var cfg pluginConfig
	if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
		return pluginConfig{}, errUnmarshal
	}
	return cfg, nil
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "cpa",
			Version:          "0.1.0",
			Author:           "imHansiy",
			GitHubRepository: "https://github.com/imHansiy/grok2api-egress-enhancements",
			Logo:             "",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enables the CPA quality guard."},
				{Name: "mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"passive", "active"}, Description: "Detection mode: passive audit or active re-probe."},
				{Name: "hard_tps", Type: pluginapi.ConfigFieldTypeNumber, Description: "Passive hard threshold in output tokens/second. Observations at or above quarantine immediately."},
				{Name: "soft_tps", Type: pluginapi.ConfigFieldTypeNumber, Description: "Passive soft threshold in output tokens/second. Below this triggers active re-probe."},
				{Name: "soft_hits_before_quarantine", Type: pluginapi.ConfigFieldTypeNumber, Description: "Consecutive soft hits after failed re-probe before quarantine."},
				{Name: "max_consecutive_errors", Type: pluginapi.ConfigFieldTypeNumber, Description: "Consecutive errors before quarantine (0 disables)."},
				{Name: "quarantine_duration", Type: pluginapi.ConfigFieldTypeString, Description: "How long a credential stays quarantined before re-probe."},
				{Name: "recovery_delay", Type: pluginapi.ConfigFieldTypeString, Description: "Extra delay before a healthy re-probe lifts quarantine."},
				{Name: "min_healthy", Type: pluginapi.ConfigFieldTypeNumber, Description: "Minimum healthy credentials that must remain; protects availability."},
				{Name: "probe_model", Type: pluginapi.ConfigFieldTypeString, Description: "Model used for active re-probes."},
				{Name: "probe_prompt", Type: pluginapi.ConfigFieldTypeString, Description: "Fixed prompt used for active re-probes."},
				{Name: "probe_expected", Type: pluginapi.ConfigFieldTypeString, Description: "Marker that must appear in the re-probe output."},
				{Name: "probe_max_output_tokens", Type: pluginapi.ConfigFieldTypeNumber, Description: "Bounds the re-probe response size."},
				{Name: "probe_timeout", Type: pluginapi.ConfigFieldTypeString, Description: "Bounds a single active re-probe."},
				{Name: "override_ttl", Type: pluginapi.ConfigFieldTypeString, Description: "TTL for the one-shot scheduler override."},
				{Name: "include_reasoning_tokens", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Output tokens include reasoning tokens (panel parity)."},
			},
		},
		Capabilities: registrationCapability{
			Scheduler:     true,
			UsagePlugin:   true,
			ManagementAPI: true,
		},
	}
}

func handleShutdown() {
	// Free any resources here; none currently held.
}
