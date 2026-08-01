package main

// probe.go implements the active re-probe used for recovery confirmation.
// A fixed streaming prompt is sent through the host model execution path while
// a one-shot scheduler override pins the credential under test. The response
// is scored with the same TPS formula as passive audit, and the expected
// marker is checked before quarantine is lifted.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// probeOutcome is the result of one active re-probe.
type probeOutcome struct {
	AuthID        string  `json:"auth_id"`
	OK            bool    `json:"ok"`
	TPS           float64 `json:"tps"`
	OutputTokens  int64   `json:"output_tokens"`
	FirstTokenMS  int64   `json:"first_token_ms"`
	DurationMS    int64   `json:"duration_ms"`
	ExpectedFound bool    `json:"expected_found"`
	Error         string  `json:"error,omitempty"`
}

// probeCredential runs an active re-probe against a specific credential using
// the host model execution path with a one-shot scheduler override.
func probeCredential(authID string, cfg *pluginConfig) probeOutcome {
	out := probeOutcome{AuthID: authID}

	// Install a one-shot override so the nested model execution selects the
	// credential under test. The override expires quickly to avoid leaking.
	store.setOverride(authID, cfg.OverrideTTL)

	body, errMarshal := json.Marshal(map[string]any{
		"model":          cfg.ProbeModel,
		"messages":       []map[string]string{{"role": "user", "content": cfg.ProbePrompt}},
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
		"max_tokens":     cfg.ProbeMaxOutputTokens,
	})
	if errMarshal != nil {
		out.Error = fmt.Sprintf("marshal probe body: %v", errMarshal)
		recordProbeFailure(authID, out.Error, cfg)
		return out
	}

	startedAt := time.Now()
	streamResp, errExecute := hostExecuteStream(cfg.ProbeModel, body, "")
	if errExecute != nil {
		out.Error = fmt.Sprintf("host.model.execute_stream: %v", errExecute)
		recordProbeFailure(authID, out.Error, cfg)
		return out
	}
	defer hostStreamClose(streamResp.StreamID)

	var visible strings.Builder
	var firstGeneratedAt time.Time
	usageSeen := false
	var outputTokens int64
	var reasoningTokens int64

	for {
		chunk, errRead := hostStreamRead(streamResp.StreamID)
		if errRead != nil {
			out.Error = fmt.Sprintf("host.model.stream_read: %v", errRead)
			recordProbeFailure(authID, out.Error, cfg)
			return out
		}
		if chunk.Error != "" {
			out.Error = fmt.Sprintf("stream error: %s", chunk.Error)
			recordProbeFailure(authID, out.Error, cfg)
			return out
		}
		if len(chunk.Payload) > 0 {
			outputTokens, reasoningTokens, firstGeneratedAt, visible = parseProbeChunk(chunk.Payload, firstGeneratedAt, visible, &usageSeen, outputTokens, reasoningTokens)
		}
		if chunk.Done {
			break
		}
	}

	completedAt := time.Now()
	durationMS := completedAt.Sub(startedAt).Milliseconds()
	firstTokenMS := int64(0)
	if !firstGeneratedAt.IsZero() {
		firstTokenMS = firstGeneratedAt.Sub(startedAt).Milliseconds()
	}
	out.DurationMS = durationMS
	out.FirstTokenMS = firstTokenMS

	// TPS uses total output tokens (including reasoning) to match the panel.
	out.OutputTokens = outputTokens
	if outputTokens <= 0 {
		// Fall back to a character-based estimate mirroring the probe design.
		chars := int64(len([]rune(visible.String())))
		if chars > 0 {
			outputTokens = (chars + 3) / 4
			out.OutputTokens = outputTokens
		}
	}
	out.TPS = outputTokensPerSecond(outputTokens, durationMS, firstTokenMS)

	text := visible.String()
	out.ExpectedFound = cfg.ProbeExpected == "" || strings.Contains(text, cfg.ProbeExpected)

	// Recovery decision: expected marker found AND speed within acceptable
	// bounds (below hard threshold). Soft threshold on probe triggers
	// consecutive-hit escalation handled by the caller/sweeper.
	if out.ExpectedFound && cfg.ProbeHardTPS > 0 && out.TPS < cfg.ProbeHardTPS {
		out.OK = true
		store.recordProbeResult(authID, true, out.TPS, "", cfg)
		return out
	}

	reason := "probe failed"
	if !out.ExpectedFound {
		reason = fmt.Sprintf("expected marker %q not found", cfg.ProbeExpected)
	} else if cfg.ProbeHardTPS > 0 && out.TPS >= cfg.ProbeHardTPS {
		reason = fmt.Sprintf("probe TPS %.0f >= hard %.0f", out.TPS, cfg.ProbeHardTPS)
	}
	out.Error = reason
	recordProbeFailure(authID, reason, cfg)
	return out
}

// recordProbeFailure updates state after a failed probe.
func recordProbeFailure(authID, errText string, cfg *pluginConfig) {
	store.recordProbeResult(authID, false, 0, errText, cfg)
}

// parseProbeChunk accumulates tokens and visible text from one SSE chunk.
func parseProbeChunk(payload []byte, firstGeneratedAt time.Time, visible strings.Builder, usageSeen *bool, outputTokens, reasoningTokens int64) (int64, int64, time.Time, strings.Builder) {
	line := strings.TrimSpace(string(payload))
	if !strings.HasPrefix(line, "data:") {
		return outputTokens, reasoningTokens, firstGeneratedAt, visible
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "[DONE]" {
		return outputTokens, reasoningTokens, firstGeneratedAt, visible
	}
	var event struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				Reasoning        string `json:"reasoning"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			CompletionTokens int64 `json:"completion_tokens"`
			PromptTokens     int64 `json:"prompt_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
			CompletionTokensDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if errUnmarshal := json.Unmarshal([]byte(data), &event); errUnmarshal != nil {
		return outputTokens, reasoningTokens, firstGeneratedAt, visible
	}
	if event.Usage != nil {
		*usageSeen = true
		outputTokens = event.Usage.CompletionTokens
		reasoningTokens = event.Usage.CompletionTokensDetails.ReasoningTokens
	}
	for _, choice := range event.Choices {
		delta := choice.Delta
		if (delta.Content != "" || delta.Reasoning != "" || delta.ReasoningContent != "") && firstGeneratedAt.IsZero() {
			firstGeneratedAt = time.Now()
		}
		if delta.Content != "" {
			visible.WriteString(delta.Content)
		}
	}
	return outputTokens, reasoningTokens, firstGeneratedAt, visible
}
