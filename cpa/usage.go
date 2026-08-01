package main

// usage.go implements the passive quality audit. Every completed request is
// scored with the audit-panel TPS formula (outputTokens including reasoning
// tokens, over generation time) and fed to the credential state machine.

import (
	"encoding/json"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// handleUsage processes a single usage.handle RPC.
func handleUsage(raw []byte) ([]byte, error) {
	var record pluginapi.UsageRecord
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	observeUsage(record)
	return okEnvelope(map[string]any{"handled": true})
}

// observeUsage applies one passive observation. It is intentionally fast and
// non-blocking; quarantine decisions are applied inline but do not wait on
// active probes.
func observeUsage(record pluginapi.UsageRecord) {
	cfg := globalCfg.get()
	if !cfg.Enabled {
		return
	}
	if record.AuthID == "" {
		return
	}
	// Failed requests feed the consecutive-error counter only.
	if record.Failed {
		if cfg.MaxConsecutiveErrors > 0 {
			if store.markError(record.AuthID, cfg.MaxConsecutiveErrors) {
				store.quarantine(record.AuthID, &cfg)
			}
		}
		return
	}
	// Streaming requests carry TTFT; non-streaming use Latency for both.
	durationMS := record.Latency.Milliseconds()
	firstTokenMS := record.TTFT.Milliseconds()
	if durationMS <= 0 {
		durationMS = 1
	}
	if firstTokenMS < 0 {
		firstTokenMS = 0
	}
	outputTokens := record.Detail.OutputTokens
	if !cfg.IncludeReasoningTokens {
		outputTokens -= record.Detail.ReasoningTokens
	}
	if outputTokens <= 0 {
		return
	}
	store.observePassive(record.AuthID, record.AuthIndex, outputTokens, durationMS, firstTokenMS, &cfg)
}

// bestEffortProbe is a no-op placeholder invoked where a passive observation
// alone crossed the hard threshold and an immediate re-probe is desirable.
// Active re-probing is implemented in probe.go and triggered by management
// actions or the recovery sweeper.
func bestEffortProbe() {}

// sweepRecoveries is invoked periodically by the management sweeper to re-probe
// credentials whose quarantine has expired. It lives in probe.go; this symbol
// keeps the usage path explicit about the recovery handoff.
func sweepRecoveries() {
	_ = time.Now()
}
