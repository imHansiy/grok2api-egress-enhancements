package main

// state_test.go verifies the credential state machine: hard-threshold
// quarantine, soft-threshold suspect marking, quarantine expiry, manual
// release, and scheduler candidate filtering.

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func testCfg() pluginConfig {
	cfg := defaultConfig()
	cfg.HardTPS = 1000
	cfg.SoftTPS = 500
	cfg.QuarantineDuration = 30 * time.Minute
	cfg.RecoveryDelay = time.Minute
	cfg.MinHealthy = 3
	return cfg
}

func TestHardThresholdQuarantinesImmediately(t *testing.T) {
	cfg := testCfg()
	st := newStateStore()

	// 2000 tokens over 1s generation = 2000 TPS >= hard 1000 -> quarantine.
	quarantined := st.observePassive("auth-1", "0", 2000, 2000, 1000, &cfg)
	if !quarantined {
		t.Fatal("expected immediate quarantine on hard threshold")
	}
	if st.isQuarantined("auth-1", time.Now()) != true {
		t.Fatal("auth-1 should be quarantined")
	}
	got := st.get("auth-1")
	if got.State != "quarantined" {
		t.Fatalf("state = %q, want quarantined", got.State)
	}
	if got.QuarantineCount != 1 {
		t.Fatalf("quarantine count = %d, want 1", got.QuarantineCount)
	}
}

func TestSoftThresholdMarksSuspectOnly(t *testing.T) {
	cfg := testCfg()
	st := newStateStore()

	// 300 tokens over 1s generation = 300 TPS < soft 500 -> suspect, no quarantine.
	quarantined := st.observePassive("auth-1", "0", 300, 2000, 1000, &cfg)
	if quarantined {
		t.Fatal("soft threshold should not quarantine immediately")
	}
	got := st.get("auth-1")
	if got.State != "suspect" {
		t.Fatalf("state = %q, want suspect", got.State)
	}
	if got.SoftHits != 1 {
		t.Fatalf("soft hits = %d, want 1", got.SoftHits)
	}
}

func TestHealthyObservationClearsSuspect(t *testing.T) {
	cfg := testCfg()
	st := newStateStore()

	st.observePassive("auth-1", "0", 300, 2000, 1000, &cfg) // suspect
	st.observePassive("auth-1", "0", 800, 2000, 1000, &cfg) // 800 TPS: healthy
	got := st.get("auth-1")
	if got.State != "healthy" {
		t.Fatalf("state = %q, want healthy", got.State)
	}
	if got.SoftHits != 0 {
		t.Fatalf("soft hits = %d, want 0", got.SoftHits)
	}
}

func TestQuarantineExpires(t *testing.T) {
	cfg := testCfg()
	cfg.QuarantineDuration = time.Second
	st := newStateStore()

	st.observePassive("auth-1", "0", 2000, 2000, 1000, &cfg) // quarantine
	if !st.isQuarantined("auth-1", time.Now()) {
		t.Fatal("expected quarantined now")
	}
	// 2 seconds later: quarantine expired -> eligible for re-probe.
	if st.isQuarantined("auth-1", time.Now().Add(2*time.Second)) {
		t.Fatal("expected quarantine to expire")
	}
}

func TestManualRelease(t *testing.T) {
	cfg := testCfg()
	st := newStateStore()

	st.observePassive("auth-1", "0", 2000, 2000, 1000, &cfg) // quarantine
	st.release("auth-1")
	if st.isQuarantined("auth-1", time.Now()) {
		t.Fatal("manual release should lift quarantine")
	}
	if got := st.get("auth-1"); got.State != "healthy" {
		t.Fatalf("state = %q, want healthy after release", got.State)
	}
}

func TestRecordProbeResultRecovers(t *testing.T) {
	cfg := testCfg()
	st := newStateStore()

	st.observePassive("auth-1", "0", 2000, 2000, 1000, &cfg) // quarantine
	st.recordProbeResult("auth-1", true, 600, "", &cfg)      // healthy probe
	if st.isQuarantined("auth-1", time.Now()) {
		t.Fatal("healthy probe result should lift quarantine")
	}
	if got := st.get("auth-1"); got.State != "healthy" {
		t.Fatalf("state = %q, want healthy", got.State)
	}
}

func TestRecordProbeResultFailureKeepsQuarantine(t *testing.T) {
	cfg := testCfg()
	st := newStateStore()

	st.observePassive("auth-1", "0", 2000, 2000, 1000, &cfg) // quarantine
	st.recordProbeResult("auth-1", false, 0, "marker not found", &cfg)
	if !st.isQuarantined("auth-1", time.Now()) {
		t.Fatal("failed probe should keep quarantine")
	}
	if got := st.get("auth-1"); got.LastProbeOK {
		t.Fatal("LastProbeOK should be false after failed probe")
	}
}

func TestSchedulerExcludesQuarantined(t *testing.T) {
	cfg := testCfg()
	// scheduler.pick reads the global store; use it directly.
	globalCfg.set(cfg)
	store = newStateStore()

	store.observePassive("auth-bad", "1", 2000, 2000, 1000, &cfg) // quarantine auth-bad

	req := pluginapi.SchedulerPickRequest{
		Provider: "grok",
		Model:    "grok-3",
		Stream:   true,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-good-1", Provider: "grok", Priority: 1, Status: "available"},
			{ID: "auth-good-2", Provider: "grok", Priority: 2, Status: "available"},
			{ID: "auth-bad", Provider: "grok", Priority: 0, Status: "available"},
		},
	}
	resp := pickAuth(req)
	if !resp.Handled {
		t.Fatal("expected handled pick")
	}
	if resp.AuthID == "auth-bad" {
		t.Fatal("scheduler must not select quarantined credential")
	}
	if resp.AuthID != "auth-good-1" && resp.AuthID != "auth-good-2" {
		t.Fatalf("unexpected pick %q", resp.AuthID)
	}
}

func TestSchedulerOverridePinsCredential(t *testing.T) {
	cfg := testCfg()
	globalCfg.set(cfg)
	store = newStateStore()

	store.setOverride("auth-target", cfg.OverrideTTL)
	req := pluginapi.SchedulerPickRequest{
		Provider: "grok",
		Model:    "grok-3",
		Stream:   true,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-other", Provider: "grok", Priority: 1, Status: "available"},
			{ID: "auth-target", Provider: "grok", Priority: 2, Status: "available"},
		},
	}
	resp := pickAuth(req)
	if resp.AuthID != "auth-target" {
		t.Fatalf("override should pin auth-target, got %q", resp.AuthID)
	}
}

func TestSchedulerEmptyHealthyDelegates(t *testing.T) {
	cfg := testCfg()
	globalCfg.set(cfg)
	store = newStateStore()

	store.observePassive("auth-1", "0", 2000, 2000, 1000, &cfg)
	store.observePassive("auth-2", "1", 2000, 2000, 1000, &cfg)

	req := pluginapi.SchedulerPickRequest{
		Provider: "grok",
		Model:    "grok-3",
		Stream:   true,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-1", Provider: "grok", Priority: 1, Status: "available"},
			{ID: "auth-2", Provider: "grok", Priority: 2, Status: "available"},
		},
	}
	resp := pickAuth(req)
	if !resp.Handled {
		t.Fatal("expected handled delegation")
	}
	if resp.DelegateBuiltin == "" {
		t.Fatal("expected delegate to builtin when no healthy candidates remain")
	}
}
