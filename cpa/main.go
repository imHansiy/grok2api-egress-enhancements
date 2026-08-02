package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* cpa_stored_host;

static void cpa_store_host_api(const cliproxy_host_api* host) {
	cpa_stored_host = host;
}

static int cpa_call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (cpa_stored_host == NULL || cpa_stored_host->call == NULL) {
		return 1;
	}
	return cpa_stored_host->call(cpa_stored_host->host_ctx, method, request, request_len, response);
}

static void cpa_free_host_buffer(void* ptr, size_t len) {
	if (cpa_stored_host != NULL && cpa_stored_host->free_buffer != NULL && ptr != NULL) {
		cpa_stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// envelope mirrors pluginabi.Envelope for host callback responses and our own
// response serialization.
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.cpa_store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func okEnvelope(result any) ([]byte, error) {
	raw, errMarshal := json.Marshal(envelope{OK: true, Result: mustRaw(result)})
	if errMarshal != nil {
		return nil, errMarshal
	}
	return raw, nil
}

func mustRaw(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func writeResponse(response *C.cliproxy_buffer, payload []byte) {
	if response == nil {
		return
	}
	if len(payload) == 0 {
		response.ptr = nil
		response.len = 0
		return
	}
	cPayload := C.CBytes(payload)
	response.ptr = cPayload
	response.len = C.size_t(len(payload))
}

// handleMethod routes each ABI method to its implementation.
func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return handleConfigure(request)
	case pluginabi.MethodPluginShutdown:
		handleShutdown()
		return okEnvelope(map[string]any{})
	case pluginabi.MethodSchedulerPick:
		return handleSchedulerPick(request)
	case pluginabi.MethodUsageHandle:
		return handleUsage(request)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration())
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// ===== C ABI host bridge =====
// All functions that touch the cgo `C` pseudo-package must live in this file,
// next to the cgo preamble and import "C".

// callHost invokes a host callback over the C ABI and returns the envelope result.
func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback payload %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.cpa_call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.cpa_free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}

	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host callback envelope %s: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

// hostExecuteStream initiates a streaming model execution through the host and
// returns the stream handle. hostCallbackID is forwarded so the host skips this
// plugin's own interceptors on the nested execution.
func hostExecuteStream(model string, body []byte, hostCallbackID string) (pluginapi.HostModelStreamResponse, error) {
	var resp pluginapi.HostModelStreamResponse
	result, errCall := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai",
			ExitProtocol:  "openai",
			Model:         model,
			Stream:        true,
			Body:          body,
			Headers:       http.Header{},
			Query:         url.Values{},
			Alt:           "",
		},
		HostCallbackID: hostCallbackID,
	})
	if errCall != nil {
		return resp, errCall
	}
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return resp, fmt.Errorf("decode host.model.execute_stream result: %w", errUnmarshal)
	}
	if resp.StreamID == "" {
		return resp, fmt.Errorf("host.model.execute_stream returned an empty stream_id")
	}
	return resp, nil
}

// hostStreamRead reads the next chunk from a host-owned model stream.
func hostStreamRead(streamID string) (pluginapi.HostModelStreamReadResponse, error) {
	var resp pluginapi.HostModelStreamReadResponse
	result, errCall := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: streamID})
	if errCall != nil {
		return resp, errCall
	}
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return resp, fmt.Errorf("decode host.model.stream_read result: %w", errUnmarshal)
	}
	return resp, nil
}

// hostStreamClose closes a host-owned model stream.
func hostStreamClose(streamID string) error {
	_, errCall := callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	return errCall
}

// hostAuthList lists all host credential records.
func hostAuthList() ([]pluginapi.HostAuthFileEntry, error) {
	var entries []pluginapi.HostAuthFileEntry
	result, errCall := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, errCall
	}
	if errUnmarshal := json.Unmarshal(result, &entries); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host.auth.list result: %w", errUnmarshal)
	}
	return entries, nil
}

// hostAuthGet returns credential JSON by auth index.
func hostAuthGet(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	var resp pluginapi.HostAuthGetResponse
	result, errCall := callHost(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return resp, errCall
	}
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return resp, fmt.Errorf("decode host.auth.get result: %w", errUnmarshal)
	}
	return resp, nil
}
