package main

// scheduler.go implements scheduler.pick. It removes quarantined credentials
// from the candidate set, honors a one-shot override (used by active re-probes
// to pin a specific credential), and otherwise delegates to the built-in
// scheduler semantics by explicitly selecting a healthy candidate.

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// handleSchedulerPick processes scheduler.pick.
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	resp := pickAuth(req)
	return okEnvelope(resp)
}

func pickAuth(req pluginapi.SchedulerPickRequest) pluginapi.SchedulerPickResponse {
	cfg := globalCfg.get()
	if !cfg.Enabled {
		return pluginapi.SchedulerPickResponse{Handled: false}
	}
	now := time.Now()

	// One-shot override: an active re-probe wants a specific credential.
	if ov := store.consumeOverride(now); ov != nil {
		for _, candidate := range req.Candidates {
			if strings.TrimSpace(candidate.ID) == ov.AuthID {
				return pluginapi.SchedulerPickResponse{AuthID: candidate.ID, Handled: true}
			}
		}
		// Override target not present; fall through to normal handling.
	}

	quarantined := store.quarantinedIDs(now)
	healthy := make([]pluginapi.SchedulerAuthCandidate, 0, len(req.Candidates))
	for _, candidate := range req.Candidates {
		if quarantined[candidate.ID] {
			continue
		}
		healthy = append(healthy, candidate)
	}

	if len(healthy) == 0 {
		// Minimum-healthy protection: if everything is quarantined, do not
		// starve the service. Delegate to the built-in scheduler so the host
		// keeps serving with whatever remains.
		return pluginapi.SchedulerPickResponse{
			DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin,
			Handled:         true,
		}
	}

	return pickFromHealthy(healthy)
}

// pickFromHealthy explicitly selects one credential from the healthy set so the
// built-in scheduler can never pick a quarantined credential.
func pickFromHealthy(healthy []pluginapi.SchedulerAuthCandidate) pluginapi.SchedulerPickResponse {
	if len(healthy) == 1 {
		return pluginapi.SchedulerPickResponse{AuthID: healthy[0].ID, Handled: true}
	}
	// Deterministic selection: lowest priority value (host preference) wins;
	// ties fall back to the first candidate.
	best := healthy[0]
	for _, candidate := range healthy[1:] {
		if candidate.Priority < best.Priority {
			best = candidate
		}
	}
	return pluginapi.SchedulerPickResponse{AuthID: best.ID, Handled: true}
}

var _ = pluginabi.MethodSchedulerPick
