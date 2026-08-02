# CPA Grok2API Egress plugin

This optional CLIProxyAPI plugin embeds a compact operations page for the
grok2api egress quality guard. It provides:

- live guard status, statistics, events, and light/dark themes;
- node add, edit, delete, enable, disable, search, and batch operations;
- connectivity checks and real-model quality tests;
- editable active/passive/hybrid guard policy;
- server-side grok2api administrator login, so proxy URLs and administrator
  credentials are never returned to the browser.

The UI intentionally manages only `grok_build` nodes. Saved proxy URLs are
write-only: editing with an empty proxy field preserves the existing value.

## Install from plugin store

This repository publishes a plugin-store registry
([`registry.json`](../registry.json)). Add it to CLIProxyAPI's
`plugins.store-sources` list:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  store-sources:
    - "https://raw.githubusercontent.com/lij768423-svg/grok2api-egress-enhancements/main/registry.json"
```

Then install through the management API:

```bash
curl -X POST -H "Authorization: Bearer <management-key>" \
  http://localhost:8317/v0/management/plugin-store/grok2api-egress/install
```

The host downloads the GitHub release assets
(`grok2api-egress_<version>_<goos>_<goarch>.zip`), verifies `checksums.txt`,
writes the plugin library, and hot-reloads. Prebuilt multi-platform assets are
published automatically by the `build-grok2api-egress` workflow when a `v*`
tag is pushed (see `.github/workflows/build-grok2api-egress.yml`).

## Build

Requirements: Go 1.26+, CGO, and a C compiler.

```sh
cd cpa-plugin/go
go test ./...
go build -buildmode=c-shared -trimpath -o grok2api-egress.so .
```

Copy the resulting `.so` into CLIProxyAPI's plugin directory and enable it:

```yaml
plugins:
  enabled: true
  configs:
    grok2api-egress:
      enabled: true
      priority: 2
      grok2api_base_url: "http://100.102.32.24:8181"
      hard_tps: 1000
      soft_tps: 500
      disable_on_hard: false
      fetch_timeout_sec: 4
```

Set `GROK2API_ADMIN_USERNAME` and `GROK2API_ADMIN_PASSWORD` only in the
CLIProxyAPI process environment. Restart CLIProxyAPI, sign in to its management
panel, then open **Grok2API Egress**. Mutations use CLIProxyAPI's authenticated
Management API; the grok2api access token is never exposed to the browser.
