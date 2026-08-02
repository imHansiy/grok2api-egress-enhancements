package main

// tps_test.go verifies the output-tokens-per-second formula matches the
// grok2api audit-panel formula exactly (output tokens including reasoning
// tokens, over generation time = duration - first token).

import "testing"

func TestOutputTokensPerSecondMatchesPanel(t *testing.T) {
	// Mirror of grok2api quality_probe_test.go:
	//   got := qualityProbeOutputTokensPerSecond(1335, 17320, 17100)
	//   want := float64(1335) * 1000 / 220
	got := outputTokensPerSecond(1335, 17320, 17100)
	want := float64(1335) * 1000 / 220
	if got != want {
		t.Fatalf("output TPS = %v, want %v", got, want)
	}
}

func TestOutputTokensPerSecondIncludesReasoningTokens(t *testing.T) {
	// Panel reports completion/output tokens, including reasoning tokens.
	got := outputTokensPerSecond(1050, 1100, 1000)
	if got != 10500 {
		t.Fatalf("output TPS = %v, want 10500", got)
	}
}

func TestOutputTokensPerSecondZeroGuards(t *testing.T) {
	if got := outputTokensPerSecond(0, 1000, 500); got != 0 {
		t.Fatalf("zero output tokens should score 0, got %v", got)
	}
	if got := outputTokensPerSecond(100, 1000, 1000); got != 0 {
		t.Fatalf("zero generation time should score 0, got %v", got)
	}
	if got := outputTokensPerSecond(100, 0, 0); got != 0 {
		t.Fatalf("zero duration should score 0, got %v", got)
	}
}
