package main

// mgmt_handlers.go implements the CPA management actions (manual quarantine,
// release, trigger probe, policy hot reload) and renders the dashboard pages.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type adminActionRequest struct {
	AuthID string `json:"auth_id"`
}

// handleAdminQuarantine manually quarantines a credential.
func handleAdminQuarantine(req managementRequest) managementResponse {
	var action adminActionRequest
	if errUnmarshal := json.Unmarshal(req.Body, &action); errUnmarshal != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	if strings.TrimSpace(action.AuthID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "auth_id required"})
	}
	cfg := globalCfg.get()
	store.quarantine(action.AuthID, &cfg)
	return jsonResponse(http.StatusOK, map[string]any{"quarantined": action.AuthID})
}

// handleAdminRelease manually lifts quarantine.
func handleAdminRelease(req managementRequest) managementResponse {
	var action adminActionRequest
	if errUnmarshal := json.Unmarshal(req.Body, &action); errUnmarshal != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	if strings.TrimSpace(action.AuthID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "auth_id required"})
	}
	store.release(action.AuthID)
	return jsonResponse(http.StatusOK, map[string]any{"released": action.AuthID})
}

// handleAdminProbe triggers an immediate active re-probe for one credential.
func handleAdminProbe(req managementRequest) managementResponse {
	var action adminActionRequest
	if errUnmarshal := json.Unmarshal(req.Body, &action); errUnmarshal != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	if strings.TrimSpace(action.AuthID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "auth_id required"})
	}
	cfg := globalCfg.get()
	out := probeCredential(action.AuthID, &cfg)
	status := http.StatusOK
	if !out.OK {
		status = http.StatusOK // report outcome; not an HTTP error
	}
	return jsonResponse(status, out)
}

// handleAdminPolicy applies a hot-reloaded policy (PUT) and returns the new config.
func handleAdminPolicy(req managementRequest) managementResponse {
	var incoming pluginConfig
	if len(req.Body) > 0 {
		if errUnmarshal := json.Unmarshal(req.Body, &incoming); errUnmarshal != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("invalid policy: %v", errUnmarshal)})
		}
	}
	normalized := normalizeConfig(incoming)
	globalCfg.set(normalized)
	return jsonResponse(http.StatusOK, normalized)
}

// renderOverviewPage renders a lightweight HTML dashboard with all credential
// states and action forms. Styling is inline; the page calls management routes
// with the stored management key from localStorage (trusted same-origin UI).
func renderOverviewPage() []byte {
	cfg := globalCfg.get()
	statuses := store.snapshot()

	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><title>CPA Quality Guard</title>")
	b.WriteString("<style>")
	b.WriteString("body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;margin:2rem;line-height:1.5;color:#1f2933;background:#fbfbfc}")
	b.WriteString("h1{font-size:1.4rem}h2{font-size:1.1rem;margin-top:1.5rem}")
	b.WriteString("table{border-collapse:collapse;width:100%;margin-top:.5rem}")
	b.WriteString("th,td{border:1px solid #d9dee3;padding:.4rem .6rem;text-align:left;font-size:.85rem}")
	b.WriteString("th{background:#f3f4f6}")
	b.WriteString(".ok{color:#15803d}.bad{color:#b42318}.warn{color:#b54708}")
	b.WriteString("button{margin-right:.4rem;padding:.25rem .6rem;font-size:.8rem;cursor:pointer}")
	b.WriteString("form{display:inline}")
	b.WriteString(".cfg{background:#f3f4f6;padding:.75rem;border-radius:6px;font-family:monospace;font-size:.8rem;white-space:pre-wrap}")
	b.WriteString("</style></head><body>")
	b.WriteString("<h1>CPA Quality Guard</h1>")

	// Policy summary.
	b.WriteString("<h2>Policy</h2><div class=\"cfg\">")
	policyJSON, _ := json.MarshalIndent(cfg, "", "  ")
	b.WriteString(string(policyJSON))
	b.WriteString("</div>")

	// Credential table.
	b.WriteString("<h2>Credentials</h2>")
	b.WriteString("<table><thead><tr><th>AuthID</th><th>State</th><th>Last TPS</th><th>Output Tokens</th><th>Soft Hits</th><th>Errors</th><th>Quarantine Until</th><th>Actions</th></tr></thead><tbody>")
	for _, st := range statuses {
		b.WriteString("<tr>")
		b.WriteString("<td>" + htmlEscape(st.AuthID) + "</td>")
		stateClass := "ok"
		if st.State == "quarantined" {
			stateClass = "bad"
		} else if st.State == "suspect" {
			stateClass = "warn"
		}
		b.WriteString("<td class=\"" + stateClass + "\">" + htmlEscape(st.State) + "</td>")
		b.WriteString(fmt.Sprintf("<td>%.0f</td>", st.LastTPS))
		b.WriteString(fmt.Sprintf("<td>%d</td>", st.LastOutputTokens))
		b.WriteString(fmt.Sprintf("<td>%d</td>", st.SoftHits))
		b.WriteString(fmt.Sprintf("<td>%d</td>", st.ConsecutiveErrors))
		b.WriteString("<td>" + htmlEscape(st.QuarantineUntil.Format("2006-01-02 15:04:05")) + "</td>")
		b.WriteString("<td>")
		b.WriteString(fmt.Sprintf("<form method=\"post\" action=\"/v0/management/plugins/cpa/release\" data-auth=\"%s\" class=\"cpa-action\"><button type=\"submit\">Release</button></form>", htmlEscapeAttr(st.AuthID)))
		b.WriteString(fmt.Sprintf("<form method=\"post\" action=\"/v0/management/plugins/cpa/quarantine\" data-auth=\"%s\" class=\"cpa-action\"><button type=\"submit\">Quarantine</button></form>", htmlEscapeAttr(st.AuthID)))
		b.WriteString(fmt.Sprintf("<form method=\"post\" action=\"/v0/management/plugins/cpa/probe\" data-auth=\"%s\" class=\"cpa-action\"><button type=\"submit\">Probe</button></form>", htmlEscapeAttr(st.AuthID)))
		b.WriteString("</td>")
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table>")

	// Management-key form: user enters the management key once; it is stored in
	// localStorage and attached to every management call. Same-origin trusted
	// pattern recommended by the plugin docs.
	b.WriteString("<h2>Management Key</h2>")
	b.WriteString("<div><label>Key: <input id=\"cpa-key\" type=\"password\" placeholder=\"management key\"></label> <button id=\"cpa-key-save\">Save</button></div>")

	b.WriteString("<script>")
	b.WriteString("const KEY='cpa_mgmt_key';")
	b.WriteString("const saved=localStorage.getItem(KEY);if(saved){document.getElementById('cpa-key').value=saved;}")
	b.WriteString("document.getElementById('cpa-key-save').onclick=()=>{localStorage.setItem(KEY,document.getElementById('cpa-key').value);alert('saved');};")
	b.WriteString("document.querySelectorAll('form.cpa-action').forEach(f=>{f.onsubmit=e=>{e.preventDefault();")
	b.WriteString("const k=localStorage.getItem(KEY)||'';const auth=f.dataset.auth;const action=f.getAttribute('action');")
	b.WriteString("fetch(action,{method:'POST',headers:{'content-type':'application/json','authorization':'Bearer '+k},body:JSON.stringify({auth_id:auth})})")
	b.WriteString(".then(r=>r.json()).then(()=>location.reload()).catch(err=>alert(err));};});")
	b.WriteString("</script>")

	b.WriteString("</body></html>")
	return []byte(b.String())
}

// renderDashboardPage is the resource root; it delegates to the overview page.
func renderDashboardPage() []byte {
	return renderOverviewPage()
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&#34;")
	return r.Replace(s)
}

func htmlEscapeAttr(s string) string {
	return htmlEscape(s)
}
