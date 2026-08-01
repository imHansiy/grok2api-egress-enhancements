package main

// management.go registers the CPA management routes and resource pages and
// dispatches management.handle requests. Sensitive operations (manual
// quarantine, release, probe, policy update) are exposed under
// /v0/management/... and require the management key; the resource page is a
// browser UI that calls those routes with the stored management key.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type managementRequest struct {
	Method         string
	Path           string
	Headers        http.Header
	Query          url.Values
	Body           []byte
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRoute struct {
	Method string `json:"Method"`
	Path   string `json:"Path"`
}

type managementRegistrationResult struct {
	Routes    []managementRoute    `json:"routes,omitempty"`
	Resources []managementResource `json:"resources,omitempty"`
}

// managementRegistration returns the registered routes/resources.
func managementRegistration() managementRegistrationResult {
	return managementRegistrationResult{
		Routes: []managementRoute{
			{Method: "GET", Path: "/plugins/cpa/status"},
			{Method: "POST", Path: "/plugins/cpa/quarantine"},
			{Method: "POST", Path: "/plugins/cpa/release"},
			{Method: "POST", Path: "/plugins/cpa/probe"},
			{Method: "GET", Path: "/plugins/cpa/policy"},
			{Method: "PUT", Path: "/plugins/cpa/policy"},
			{Method: "GET", Path: "/plugins/cpa/overview"},
		},
		Resources: []managementResource{
			{Path: "/", Menu: "CPA Quality Guard", Description: "Credential quality-guard dashboard."},
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
		}
	}
	resp := dispatchManagement(req)
	return okEnvelope(resp)
}

func dispatchManagement(req managementRequest) managementResponse {
	path := strings.TrimPrefix(req.Path, "/v0/management")
	path = strings.TrimPrefix(path, "/v0/resource/plugins/cpa")
	path = strings.TrimPrefix(path, "/plugins/cpa")
	if path == "" || path == "/" {
		// Resource root: dashboard page.
		if strings.HasPrefix(req.Path, "/v0/resource") || req.Method == "GET" {
			return managementResponse{
				StatusCode: http.StatusOK,
				Headers:    htmlHeaders(),
				Body:       renderDashboardPage(),
			}
		}
	}
	switch {
	case req.Method == "GET" && path == "/status":
		return jsonResponse(http.StatusOK, map[string]any{
			"enabled":  globalCfg.get().Enabled,
			"mode":     globalCfg.get().Mode,
			"statuses": store.snapshot(),
		})
	case req.Method == "POST" && path == "/quarantine":
		return handleAdminQuarantine(req)
	case req.Method == "POST" && path == "/release":
		return handleAdminRelease(req)
	case req.Method == "POST" && path == "/probe":
		return handleAdminProbe(req)
	case req.Method == "GET" && path == "/policy":
		return jsonResponse(http.StatusOK, globalCfg.get())
	case req.Method == "PUT" && path == "/policy":
		return handleAdminPolicy(req)
	case req.Method == "GET" && path == "/overview":
		return managementResponse{
			StatusCode: http.StatusOK,
			Headers:    htmlHeaders(),
			Body:       renderOverviewPage(),
		}
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func htmlHeaders() http.Header {
	return http.Header{"content-type": []string{"text/html; charset=utf-8"}}
}

func jsonResponse(status int, v any) managementResponse {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		raw = []byte(`{"error":"marshal failed"}`)
		status = http.StatusInternalServerError
	}
	return managementResponse{
		StatusCode: status,
		Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
		Body:       raw,
	}
}
