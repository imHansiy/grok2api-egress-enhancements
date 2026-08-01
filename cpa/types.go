package main

// types.go defines the plugin registration envelope types and the global
// runtime state shared across capability files.

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRegistrar         bool `json:"model_registrar"`
	ModelProvider          bool `json:"model_provider"`
	AuthProvider           bool `json:"auth_provider"`
	FrontendAuthProvider   bool `json:"frontend_auth_provider"`
	Executor               bool `json:"executor"`
	RequestTranslator      bool `json:"request_translator"`
	RequestNormalizer      bool `json:"request_normalizer"`
	ResponseTranslator     bool `json:"response_translator"`
	ResponseInterceptor    bool `json:"response_interceptor"`
	StreamChunkInterceptor bool `json:"stream_chunk_interceptor"`
	Scheduler              bool `json:"scheduler"`
	ModelRouter            bool `json:"model_router"`
	UsagePlugin            bool `json:"usage_plugin"`
	CommandLinePlugin      bool `json:"command_line_plugin"`
	ManagementAPI          bool `json:"management_api"`
}

// Global runtime state for the plugin.
var (
	globalCfg = newConfigState()
	store     = newStateStore()
	// usageBusy serializes passive usage observation processing.
	usageBusy = false
)
