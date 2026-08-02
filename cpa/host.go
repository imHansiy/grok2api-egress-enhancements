package main

// host.go defines pure-Go helpers and type aliases for the active re-probe
// path. The actual C ABI bridge (callHost and friends) lives in main.go next
// to the cgo preamble, because the cgo `C` pseudo-package and the C static
// host pointer are only reachable from the file that imports "C".

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// hostModelExecutionRequest is pluginabi.HostModelExecutionRequest plus the
// HostCallbackID that must be forwarded when calling from management.handle.
type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// hostAuthListResponse is the raw envelope result for host.auth.list.
type hostAuthListResponse []pluginapi.HostAuthFileEntry
