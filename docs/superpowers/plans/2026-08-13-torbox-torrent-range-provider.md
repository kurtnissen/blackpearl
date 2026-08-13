# TorBox Torrent Range Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a read-only TorBox torrent-file adapter that serves exact arbitrary ranges through BlackPearl's existing rolling cache without persisting secrets or expiring CDN URLs.

**Architecture:** `gateway/torbox` implements the existing `cache.RangeOpener` contract. It maps canonical `torrent-id:file-id` references to immutable account metadata, coalesces and caches short-lived TorBox CDN links in memory, and returns strict range sources. Typed configuration and `cmd/blackpearl` select either the existing HTTP origin or TorBox without changing core, PearlFS, PearlNFS, or cache policy.

**Tech Stack:** Go 1.24+, standard `net/http`, `encoding/json`, `httptest`, existing rolling cache, Docker Compose, `go test -race`, `go vet`.

## Global Constraints

- Only already-complete torrent files in the user's TorBox account are supported.
- Never search public indexes or add, modify, pause, resume, seed, or delete TorBox items.
- Never persist or log the API token or generated CDN URL.
- Object IDs use canonical positive decimal `<torrent-id>:<file-id>` form.
- TorBox API calls use the configured API origin; production configuration requires HTTPS.
- CDN responses must provide exact HTTP 206 ranges; HTTP 200 is rejected.
- All I/O takes `context.Context` first and wraps errors with operation context.
- Existing persistent, HTTP rolling, FUSE, NFS, Plex, and Compose behavior must remain green.
- Mocked contract evidence and live-provider evidence remain explicitly distinct.

## File map

- Create `internal/gateway/torbox/object.go`: canonical object-ID parsing and immutable file metadata.
- Create `internal/gateway/torbox/gateway.go`: API configuration, metadata lookup, link cache/coalescing, and source construction.
- Create `internal/gateway/torbox/source.go`: strict CDN `RangeSource` implementation and one-time expired-link refresh.
- Create matching `_test.go` files beside each production file.
- Modify `internal/config/config.go` and tests: provider selection and TorBox settings.
- Modify `cmd/blackpearl/app.go` and tests: dependency wiring only.
- Modify `.env.example`, `README.md`, `docs/architecture.md`, and acceptance docs.
- Create `scripts/verify-torbox-live.sh`: opt-in, secret-safe provider probe.

---

### Task 1: Canonical TorBox object identity

**Files:**
- Create: `internal/gateway/torbox/object.go`
- Test: `internal/gateway/torbox/object_test.go`

**Interfaces:**
- Produces: `parseObjectID(string) (objectID, error)` where `objectID` holds `TorrentID int64` and `FileID int64`.
- Produces: `objectID.String() string` returning canonical `<torrent-id>:<file-id>`.

- [ ] **Step 1: Write failing table tests**

Test valid IDs such as `1:2` and `9223372036854775807:9`, and reject `0:1`, `1:0`, negative values, whitespace, leading zeros, extra separators, overflow, and non-digits. Assert canonical round-trip output.

- [ ] **Step 2: Verify RED**

Run `go test ./internal/gateway/torbox -run TestParseObjectID -count=1`; expect a compile failure because `parseObjectID` does not exist.

- [ ] **Step 3: Implement the parser**

Split exactly once on `:`, require each component to match `[1-9][0-9]*`, parse with `strconv.ParseInt(..., 10, 64)`, and wrap component-specific errors.

- [ ] **Step 4: Verify GREEN and commit**

Run `go test ./internal/gateway/torbox -run TestParseObjectID -count=1`, then commit `feat: define TorBox object identity`.

### Task 2: TorBox account metadata contract

**Files:**
- Create: `internal/gateway/torbox/gateway.go`
- Test: `internal/gateway/torbox/gateway_test.go`

**Interfaces:**
- Produces: `Options{APIBaseURL string, APIToken string, MetadataTTL time.Duration, LinkTTL time.Duration}`.
- Produces: `New(options Options, client *http.Client) (*Gateway, error)`.
- Produces: `Gateway.Ready(context.Context) error`.
- Internal metadata: exact size and `torbox:<hash-type>:<hash>:<size>` validator.

- [ ] **Step 1: Write failing API contract tests**

Use `httptest.Server` to require `GET /v1/api/torrents/mylist?id=17&bypass_cache=true` and `Authorization: Bearer test-token`. Return the documented TorBox envelope with one completed torrent and file. Assert `Open` returns the exact size and validator.

Add separate table cases for HTTP errors, `success=false`, wrong torrent/file IDs, `download_finished=false`, `download_present=false`, `zipped=true`, `infected=true`, zero size, missing hash/MD5, malformed JSON, oversized JSON, and context cancellation. Assert errors never contain `test-token`.

- [ ] **Step 2: Verify RED**

Run `go test ./internal/gateway/torbox -run 'TestGateway(Open|Rejects)' -count=1`; expect missing `New`/`Gateway` failures.

- [ ] **Step 3: Implement bounded metadata lookup**

Validate the API base and token, clone the injected client, disable redirects, construct requests with `url.JoinPath`, cap JSON bodies at 1 MiB, decode the TorBox envelope, sanitize `detail` to 256 printable characters, and map the exact account file. Prefer `hash`, then `md5`, and include size in the validator.

- [ ] **Step 4: Add metadata TTL tests and implementation**

Inject a clock function in unexported gateway state. Prove two opens inside 60 seconds use one `mylist` request and an open after expiry performs another. Cache metadata in memory only by canonical object ID.

- [ ] **Step 5: Verify GREEN and commit**

Run `go test -race ./internal/gateway/torbox -count=1`, then commit `feat: map TorBox torrent metadata`.

### Task 3: Short-lived download link generation and coalescing

**Files:**
- Modify: `internal/gateway/torbox/gateway.go`
- Modify: `internal/gateway/torbox/gateway_test.go`

**Interfaces:**
- Internal: `Gateway.downloadURL(context.Context, objectID, validator, force bool) (*url.URL, error)`.
- Link cache key combines canonical object ID and validator; values never leave process memory.

- [ ] **Step 1: Write failing link contract tests**

Require `GET /v1/api/torrents/requestdl?torrent_id=17&file_id=3&redirect=false&append_name=false&token=test-token` plus bearer authentication. Return the documented envelope with an HTTPS CDN URL. Assert the CDN receives no Authorization header and no token query parameter.

Reject unsuccessful envelopes, blank or non-HTTPS data URLs, fragments/userinfo, redirects, malformed JSON, and errors containing the token or CDN URL.

- [ ] **Step 2: Verify RED**

Run `go test ./internal/gateway/torbox -run 'TestGatewayDownload|TestGatewayRejectsDownload' -count=1`; expect missing link-resolution behavior.

- [ ] **Step 3: Implement in-memory TTL cache**

Generate links only from the configured TorBox API host, store parsed URLs keyed by object+validator for two hours, and never include URLs in error text.

- [ ] **Step 4: Add and pass concurrent coalescing test**

Block the requestdl handler, launch at least 16 same-object opens/reads, release it, and assert exactly one requestdl call. Implement a per-key in-flight call with cancellable waiters and lifecycle independent of any single waiter.

- [ ] **Step 5: Verify GREEN and commit**

Run `go test -race ./internal/gateway/torbox -count=1`, then commit `feat: resolve TorBox download links`.

### Task 4: Strict CDN range source and link refresh

**Files:**
- Create: `internal/gateway/torbox/source.go`
- Test: `internal/gateway/torbox/source_test.go`

**Interfaces:**
- Produces an `acquisition.RangeSource` from `Gateway.Open` with `ReadAt`, `Size`, `Validator`, and `Close`.
- `ReadAt` refreshes and retries exactly once for CDN status 401, 403, or 410.

- [ ] **Step 1: Write failing exact-read tests**

Serve a byte array from a TLS `httptest.Server`. Require exact `Range` headers and return 206 with exact `Content-Range`. Test interior reads, final partial reads returning `io.EOF`, reads at EOF, empty buffers, negative offsets, cancellation, and closed sources.

- [ ] **Step 2: Verify RED**

Run `go test ./internal/gateway/torbox -run TestSourceReadAt -count=1`; expect missing source behavior.

- [ ] **Step 3: Implement strict range validation**

Reuse equivalent local parsing behavior to `httporigin` without importing gateway internals: exact 206, exact start/end/size, body limited to wanted+1, and exact length. Disable CDN redirects on a cloned client.

- [ ] **Step 4: Add invalid-response and refresh tests**

Reject HTTP 200, wrong/malformed `Content-Range`, short/oversized bodies, and HEAD/content-length mismatch. For each of 401/403/410, assert one forced requestdl refresh and one retry succeeds. Assert a second expiry fails without a third request. Assert 500 does not refresh.

- [ ] **Step 5: Verify GREEN and commit**

Run `go test -race ./internal/gateway/torbox -count=1`, then commit `feat: stream strict TorBox ranges`.

### Task 5: Typed provider configuration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `.env.example`

**Interfaces:**
- Adds `RangeProvider string` default `http-range`.
- Adds `TorBoxAPIURL string` default `https://api.torbox.app/v1/api/`.
- Adds `TorBoxAPIToken string` with no default.

- [ ] **Step 1: Write failing configuration tests**

Prove existing HTTP rolling config remains valid by default. Prove `torbox-torrent` requires a token, canonical object ID, and HTTPS TorBox API URL; reject TorBox settings in persistent mode and reject unknown providers.

- [ ] **Step 2: Verify RED**

Run `go test ./internal/config -run 'TestParse.*(Rolling|TorBox)' -count=1`; expect missing fields/validation failures.

- [ ] **Step 3: Implement provider-specific validation**

Branch rolling validation by `RangeProvider`: require HTTP origin/object for `http-range`; require API URL/token/object for `torbox-torrent`; reject mutually exclusive provider settings. Ensure errors mention variable names but never token values.

- [ ] **Step 4: Verify GREEN and commit**

Run `go test ./internal/config -count=1`, then commit `feat: configure TorBox range provider`.

### Task 6: Single-binary runtime wiring

**Files:**
- Modify: `cmd/blackpearl/app.go`
- Modify: `cmd/blackpearl/app_test.go`

**Interfaces:**
- `run` selects a `cache.RangeOpener` from config and passes it unchanged to `cache.NewRolling`.
- TorBox backing provider is `torbox-torrent`; HTTP backing remains `http-range`.

- [ ] **Step 1: Write failing wiring tests**

Use injected HTTP servers and the existing fake NFS/server dependencies. Assert TorBox mode registers the configured logical size/provider/object and starts without invoking HTTP-origin behavior. Assert a TorBox API failure is wrapped without exposing the token.

- [ ] **Step 2: Verify RED**

Run `go test ./cmd/blackpearl -run TestRunRollingTorBox -count=1`; expect unsupported provider behavior.

- [ ] **Step 3: Implement narrow provider factory wiring**

Extract a focused `newRangeOpener(cfg, client)` function in `app.go` or a dedicated wiring file if needed. Construct `httporigin.Gateway` or `torbox.Gateway`, then retain the existing catalog/rolling-cache flow.

- [ ] **Step 4: Verify GREEN and commit**

Run `go test -race ./cmd/blackpearl ./internal/...`, then commit `feat: run rolling cache with TorBox`.

### Task 7: Secret-safe opt-in live verification and documentation

**Files:**
- Create: `scripts/verify-torbox-live.sh`
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/acceptance-evidence.md`

**Interfaces:**
- Script consumes `BLACKPEARL_TORBOX_API_TOKEN` and `BLACKPEARL_RANGE_OBJECT_ID` from environment.
- Script prints only named PASS/FAIL gates and never echoes secrets or signed URLs.

- [ ] **Step 1: Add shell contract checks**

The script must fail before network access when either variable is missing, validate canonical object ID locally, run a metadata/open/range probe through a temporary BlackPearl process or focused Go live test, compare two non-sequential ranges, and print `torbox_live_metadata=PASS` and `torbox_live_ranges=PASS` only after exact success.

- [ ] **Step 2: Document operational status precisely**

Document setup variables, read-only scope, API rate-limit considerations, and the difference between mocked-contract pass and live TorBox pass. Do not provide or log example real tokens.

- [ ] **Step 3: Verify scripts and docs**

Run `bash -n scripts/verify-torbox-live.sh`, verify the missing-secret failure path, `git diff --check`, and all Compose safety scripts.

- [ ] **Step 4: Commit**

Commit `docs: add TorBox provider runbook`.

### Task 8: Full regression, independent review, and integration

**Files:**
- Modify only files required by review findings.

- [ ] **Step 1: Run the complete local suite**

Run:

```bash
go vet ./...
go test -race -coverprofile=coverage.out ./...
./scripts/check-coverage.sh coverage.out
./scripts/test-compose-paths.sh
./scripts/test-portable-compose.sh
./scripts/test-rolling-compose.sh
./scripts/verify-portable-poc.sh
./scripts/verify-rolling-poc.sh
git diff --check
```

Require zero failures and at least 80% aggregate coverage.

- [ ] **Step 2: Request independent review**

Review the feature range against the design for secret leakage, SSRF/redirect behavior, race safety, TTL/coalescing correctness, range strictness, stale validator mixing, and evidence overclaims. Fix all Critical and Important findings test-first.

- [ ] **Step 3: Re-run fresh verification**

Repeat the full suite after the final fix commit. If live credentials are absent, record TorBox as contract-verified and live-provider pending.

- [ ] **Step 4: Integrate locally**

Fast-forward the verified feature branch into local `main`, rerun the merged test suite, and clean up only the feature worktree created for this plan.
