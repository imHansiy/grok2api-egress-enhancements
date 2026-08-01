package main

// state.go implements the quality-guard credential state machine:
// passive soft/hard TPS thresholds, quarantine, cooldown, and recovery,
// mirroring the grok2api egress quality guard design.

import (
	"sort"
	"sync"
	"time"
)

// credentialStatus tracks the quality-guard lifecycle of one auth record.
type credentialStatus struct {
	// AuthID identifies the credential (scheduler candidate ID).
	AuthID string `json:"auth_id"`
	// AuthIndex is the runtime credential index used for host.auth.get.
	AuthIndex string `json:"auth_index,omitempty"`
	// Provider is the credential provider key.
	Provider string `json:"provider,omitempty"`

	// State is one of "healthy", "suspect", "quarantined", "recovering".
	State string `json:"state"`

	// SoftHits counts consecutive soft-threshold violations.
	SoftHits int `json:"soft_hits"`
	// ConsecutiveErrors counts consecutive probe/request errors.
	ConsecutiveErrors int `json:"consecutive_errors"`

	// QuarantinedAt is when the credential was last quarantined.
	QuarantinedAt time.Time `json:"quarantined_at,omitempty"`
	// QuarantineUntil is when the credential may be re-probed.
	QuarantineUntil time.Time `json:"quarantine_until,omitempty"`
	// RecoverAt is the earliest time a healthy re-probe result may lift quarantine.
	RecoverAt time.Time `json:"recover_at,omitempty"`

	// LastTPS is the most recent output tokens-per-second observation.
	LastTPS float64 `json:"last_tps,omitempty"`
	// LastOutputTokens is the most recent output token count.
	LastOutputTokens int64 `json:"last_output_tokens,omitempty"`
	// LastDurationMS and LastFirstTokenMS mirror the audit-panel TPS formula.
	LastDurationMS    int64 `json:"last_duration_ms,omitempty"`
	LastFirstTokenMS  int64 `json:"last_first_token_ms,omitempty"`
	LastObservedAt    time.Time `json:"last_observed_at,omitempty"`

	// LastProbeError holds the last active-probe error message (diagnostics only).
	LastProbeError string `json:"last_probe_error,omitempty"`
	// LastProbeOK is true when the most recent active re-probe passed.
	LastProbeOK bool `json:"last_probe_ok,omitempty"`
	// LastProbeAt records the last active re-probe time.
	LastProbeAt time.Time `json:"last_probe_at,omitempty"`

	// ObservedCount is the total number of passive observations.
	ObservedCount int64 `json:"observed_count"`
	// QuarantineCount is the total number of quarantine episodes.
	QuarantineCount int64 `json:"quarantine_count"`
}

// stateStore is the concurrency-safe credential state registry.
type stateStore struct {
	mu       sync.RWMutex
	statuses map[string]*credentialStatus
	// override is a one-shot scheduler override set by an active probe so the
	// host routes a nested model execution to a specific credential.
	override *schedulerOverride
}

type schedulerOverride struct {
	AuthID    string
	ExpiresAt time.Time
	Triggered bool
}

func newStateStore() *stateStore {
	return &stateStore{statuses: map[string]*credentialStatus{}}
}

// ensure returns the status entry for authID, creating it if needed.
func (s *stateStore) ensure(authID string) *credentialStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureLocked(authID)
}

func (s *stateStore) ensureLocked(authID string) *credentialStatus {
	st, ok := s.statuses[authID]
	if !ok {
		st = &credentialStatus{AuthID: authID, State: "healthy"}
		s.statuses[authID] = st
	}
	return st
}

// get returns a copy of the status for authID, or nil.
func (s *stateStore) get(authID string) *credentialStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.statuses[authID]
	if !ok {
		return nil
	}
	cp := *st
	return &cp
}

// snapshot returns all statuses sorted by AuthID (for management UI).
func (s *stateStore) snapshot() []credentialStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]credentialStatus, 0, len(s.statuses))
	for _, st := range s.statuses {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AuthID < out[j].AuthID })
	return out
}

// observePassive records a passive usage observation and applies thresholds.
// It returns true when the credential should be quarantined immediately.
func (s *stateStore) observePassive(authID, authIndex string, outputTokens int64, durationMS, firstTokenMS int64, cfg *pluginConfig) bool {
	tps := outputTokensPerSecond(outputTokens, durationMS, firstTokenMS)
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensureLocked(authID)
	st.AuthIndex = authIndex
	st.LastTPS = tps
	st.LastOutputTokens = outputTokens
	st.LastDurationMS = durationMS
	st.LastFirstTokenMS = firstTokenMS
	st.LastObservedAt = time.Now()
	st.ObservedCount++

	if st.State == "quarantined" {
		// Passive observations during quarantine do not re-trigger.
		return false
	}

	// Hard threshold: immediate quarantine.
	if cfg.HardTPS > 0 && tps >= cfg.HardTPS {
		st.State = "quarantined"
		st.SoftHits = 0
		st.ConsecutiveErrors = 0
		st.QuarantinedAt = time.Now()
		st.QuarantineUntil = st.QuarantinedAt.Add(cfg.QuarantineDuration)
		st.RecoverAt = st.QuarantinedAt.Add(cfg.RecoveryDelay)
		st.QuarantineCount++
		return true
	}

	// Soft threshold: mark suspect; repeated hits after re-probe quarantine.
	if cfg.SoftTPS > 0 && tps < cfg.SoftTPS {
		if st.State != "suspect" {
			st.State = "suspect"
			st.SoftHits = 0
		}
		st.SoftHits++
		return false
	}

	// Healthy observation clears suspicion.
	if st.State == "suspect" {
		st.State = "healthy"
		st.SoftHits = 0
	}
	st.ConsecutiveErrors = 0
	return false
}

// markError records a failed request or probe.
func (s *stateStore) markError(authID string, maxConsecutive int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensureLocked(authID)
	st.ConsecutiveErrors++
	return maxConsecutive > 0 && st.ConsecutiveErrors >= maxConsecutive
}

// quarantine forcibly quarantines a credential (admin or probe failure).
func (s *stateStore) quarantine(authID string, cfg *pluginConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensureLocked(authID)
	st.State = "quarantined"
	st.QuarantinedAt = time.Now()
	st.QuarantineUntil = st.QuarantinedAt.Add(cfg.QuarantineDuration)
	st.RecoverAt = st.QuarantinedAt.Add(cfg.RecoveryDelay)
	st.QuarantineCount++
	st.ConsecutiveErrors = 0
}

// release manually lifts quarantine (admin action).
func (s *stateStore) release(authID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.statuses[authID]; ok {
		st.State = "healthy"
		st.SoftHits = 0
		st.ConsecutiveErrors = 0
		st.QuarantineUntil = time.Time{}
		st.RecoverAt = time.Time{}
		st.LastProbeOK = false
	}
}

// recordProbeResult applies an active re-probe outcome.
func (s *stateStore) recordProbeResult(authID string, ok bool, tps float64, errText string, cfg *pluginConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensureLocked(authID)
	st.LastProbeAt = time.Now()
	st.LastProbeOK = ok
	st.LastTPS = tps
	if errText != "" {
		st.LastProbeError = errText
	}
	if ok {
		st.State = "healthy"
		st.SoftHits = 0
		st.ConsecutiveErrors = 0
		st.QuarantineUntil = time.Time{}
		st.RecoverAt = time.Time{}
		st.LastProbeError = ""
		return
	}
	// Failed re-probe: keep or extend quarantine.
	if st.State != "quarantined" {
		st.State = "quarantined"
		st.QuarantinedAt = time.Now()
		st.QuarantineUntil = st.QuarantinedAt.Add(cfg.QuarantineDuration)
		st.RecoverAt = st.QuarantinedAt.Add(cfg.RecoveryDelay)
		st.QuarantineCount++
	}
	st.ConsecutiveErrors++
}

// isQuarantined reports whether authID is currently quarantined.
func (s *stateStore) isQuarantined(authID string, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.statuses[authID]
	if !ok {
		return false
	}
	if st.State != "quarantined" {
		return false
	}
	// Quarantine is time-boxed; once expired the credential becomes eligible
	// for active re-probe (recovering) rather than staying excluded.
	return now.Before(st.QuarantineUntil)
}

// quarantinedIDs returns the set of currently quarantined auth IDs.
func (s *stateStore) quarantinedIDs(now time.Time) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]bool{}
	for id, st := range s.statuses {
		if st.State == "quarantined" && now.Before(st.QuarantineUntil) {
			out[id] = true
		}
	}
	return out
}

// setOverride installs a one-shot scheduler override for an active probe.
func (s *stateStore) setOverride(authID string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.override = &schedulerOverride{AuthID: authID, ExpiresAt: time.Now().Add(ttl)}
}

// consumeOverride returns and clears a fresh scheduler override, if any.
func (s *stateStore) consumeOverride(now time.Time) *schedulerOverride {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.override == nil {
		return nil
	}
	if now.After(s.override.ExpiresAt) {
		s.override = nil
		return nil
	}
	ov := s.override
	s.override = nil
	return ov
}

// outputTokensPerSecond matches the grok2api audit-panel formula:
// outputTokens * 1000 / (durationMS - firstTokenMS), where outputTokens
// intentionally includes reasoning tokens.
func outputTokensPerSecond(outputTokens, durationMS, firstTokenMS int64) float64 {
	generationMS := durationMS - firstTokenMS
	if outputTokens <= 0 || generationMS <= 0 {
		return 0
	}
	return float64(outputTokens) * 1000 / float64(generationMS)
}
