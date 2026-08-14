# On-demand Acquisition Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add validated media-intent and release types, a read-only Prowlarr search gateway, and deterministic provider-neutral release ranking.

**Architecture:** `internal/acquisition` owns zero-infrastructure value objects. `internal/gateway/prowlarr` maps the external HTTP API into those values. `internal/resolver` fans out across search providers, tolerates partial provider failure, deduplicates, and ranks results without importing Prowlarr or TorBox.

**Tech Stack:** Go 1.24, standard `net/http`, `httptest`, `testify`, race detector, existing OpenTelemetry HTTP transport.

## Global Constraints

- Search is read-only and must not mutate TorBox, Prowlarr, Plex, or any download client.
- API keys, download URLs, and magnet URLs must never appear in errors or logs.
- Titles are at most 200 UTF-8 bytes and contain no control characters.
- Prowlarr response bodies are capped at 8 MiB and redirects are rejected.
- Existing filesystem, cache, browser setup, and live Plex behavior remain unchanged.
- Production media and Plex library paths are never inspected or modified.

---

### Task 1: Validated search and release domain

**Files:**
- Create: `internal/acquisition/search.go`
- Create: `internal/acquisition/search_test.go`

**Interfaces:**
- Produces: `NewMovieSearch(title string, year int) (SearchRequest, error)`
- Produces: `NewEpisodeSearch(showTitle string, year, season, episode int) (SearchRequest, error)`
- Produces: `(SearchRequest).Query() string`
- Produces: `NewRelease(ReleaseInput) (Release, error)`
- Produces: `ReleaseProtocol`, `ProviderCapabilities`, `ReleaseInput`, and immutable `Release` accessors

- [ ] **Step 1: Write failing request validation tests**

Cover valid movie/episode queries, trimmed titles, 200-byte bounds, control
characters, year bounds, season bounds, and episode bounds. Assert exact query
strings: `Otherhood 2019` and `Friends S07E02`.

- [ ] **Step 2: Run request tests and verify RED**

Run: `go test ./internal/acquisition -run 'TestNew(Movie|Episode)Search'`

Expected: compile failure because the constructors and types do not exist.

- [ ] **Step 3: Implement request values**

Create an immutable-by-convention struct with unexported fields and accessors:

```go
type SearchRequest struct {
    mediaType domain.MediaType
    title string
    year int
    season int
    episode int
}

func NewMovieSearch(title string, year int) (SearchRequest, error)
func NewEpisodeSearch(showTitle string, year int, season int, episode int) (SearchRequest, error)
func (r SearchRequest) Query() string
```

Use one private title validator; do not reuse the domain path validator because
search titles are not paths.

- [ ] **Step 4: Run request tests and verify GREEN**

Run: `go test ./internal/acquisition -run 'TestNew(Movie|Episode)Search'`

Expected: PASS.

- [ ] **Step 5: Write failing release validation tests**

Cover torrent-by-hash, torrent-by-magnet, usenet-by-download-URL, unsupported
protocols, missing locators, invalid hashes, unsafe magnet URLs, credentialed
HTTP URLs, zero size, negative seeders, and source/indexer/title bounds.

- [ ] **Step 6: Run release tests and verify RED**

Run: `go test ./internal/acquisition -run TestNewRelease`

Expected: compile failure because release values do not exist.

- [ ] **Step 7: Implement release values and capabilities**

Use constructors and accessors so gateway output is always valid:

```go
type ReleaseProtocol string
const (
    ReleaseProtocolTorrent ReleaseProtocol = "torrent"
    ReleaseProtocolUsenet ReleaseProtocol = "usenet"
)

type ProviderCapabilities struct {
    Protocols []ReleaseProtocol
    InfoHashes bool
    MagnetURLs bool
    DownloadURLs bool
}

type ReleaseInput struct {
    SourceID string
    Title string
    Protocol ReleaseProtocol
    Size int64
    Indexer string
    InfoHash string
    MagnetURL string
    DownloadURL string
    Seeders *int
}

func NewRelease(input ReleaseInput) (Release, error)
```

Normalize hexadecimal SHA-1 hashes to lowercase. Accept 32-character base32
BTIH values without rewriting them. Parse locators but retain their original
string only inside the release value.

- [ ] **Step 8: Run acquisition tests**

Run: `go test -race ./internal/acquisition`

Expected: PASS.

- [ ] **Step 9: Commit domain values**

```bash
git add internal/acquisition/search.go internal/acquisition/search_test.go
git commit -m "feat: model acquisition search releases"
```

### Task 2: Read-only Prowlarr gateway

**Files:**
- Create: `internal/gateway/prowlarr/gateway.go`
- Create: `internal/gateway/prowlarr/gateway_test.go`

**Interfaces:**
- Consumes: `acquisition.SearchRequest`, `acquisition.NewRelease`
- Produces: `New(Options, *http.Client) (*Gateway, error)`
- Produces: `(Gateway).Name() string`, `(Gateway).Capabilities() acquisition.ProviderCapabilities`
- Produces: `(Gateway).Search(context.Context, acquisition.SearchRequest) ([]acquisition.Release, error)`

- [ ] **Step 1: Write failing constructor and request contract tests**

Use `httptest.NewTLSServer`. Prove a path-prefix base URL produces
`/prowlarr/api/v1/search`, `query`, `type=search`, `limit=100`, and exactly one
`X-Api-Key` header. Reject nil clients, empty/whitespace/oversize keys,
credentialed URLs, query/fragment URLs, and non-HTTP schemes.

- [ ] **Step 2: Run gateway contract tests and verify RED**

Run: `go test ./internal/gateway/prowlarr -run 'Test(New|SearchSends)'`

Expected: compile failure because the package does not exist.

- [ ] **Step 3: Implement constructor and request path**

Create an isolated copy of the supplied client with redirects rejected. Use
`url.JoinPath`, `http.NewRequestWithContext`, `X-Api-Key`, and a fixed result
limit. Never embed the API key in a URL.

- [ ] **Step 4: Write failing response mapping tests**

Return a mixed JSON array containing valid torrent, valid usenet, malformed,
and unsupported records. Assert only valid normalized releases survive. Cover
HTTP 401/403 as `domain.ErrUnauthorized`, other non-200 statuses, invalid JSON,
an oversized body, cancellation, redirects, and that errors do not contain the
API key, magnet, or download URL.

- [ ] **Step 5: Run response tests and verify RED**

Run: `go test ./internal/gateway/prowlarr -run 'TestSearch'`

Expected: failures for missing response decoding and error mapping.

- [ ] **Step 6: Implement bounded response mapping**

Decode only the documented Prowlarr fields into a private wire struct. Convert
`protocol` case-insensitively, synthesize source ID from `guid` or
`indexerId:id`, and pass every result through `acquisition.NewRelease`. Skip
individual invalid records; fail only the request boundary itself.

- [ ] **Step 7: Run gateway race tests**

Run: `go test -race ./internal/gateway/prowlarr`

Expected: PASS.

- [ ] **Step 8: Commit gateway**

```bash
git add internal/gateway/prowlarr
git commit -m "feat: search authorized Prowlarr indexers"
```

### Task 3: Best-effort resolver and deterministic ranking

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `internal/resolver/service.go`
- Modify: `internal/resolver/service_test.go`
- Create: `internal/resolver/ranking.go`
- Create: `internal/resolver/ranking_test.go`

**Interfaces:**
- Consumes: providers implementing `Search`, `Name`, and `Capabilities`
- Produces: `NewSearcher(providers ...SearchProvider) *Service`
- Produces: `(Service).Search(context.Context, acquisition.SearchRequest) ([]acquisition.Release, error)`

- [ ] **Step 1: Write failing fan-out tests**

Cover no providers, one successful provider, partial provider failure, every
provider failing, cancellation, and stable provider error names. Assert a
partial result is returned without an error when at least one provider
succeeds.

- [ ] **Step 2: Run fan-out tests and verify RED**

Run: `go test ./internal/resolver -run TestSearch`

Expected: compile failure because `Search` and its provider boundary do not
exist.

- [ ] **Step 3: Implement best-effort search**

Keep the existing backing-object `Resolve` API unchanged. Add a separate
consumer-owned `SearchProvider` interface and `NewSearcher` constructor. Search
providers concurrently with `errgroup.WithContext`, collect results under a
mutex, and return validated partial results when any provider succeeds.
Promote the already-resolved `golang.org/x/sync` module to a direct dependency.

- [ ] **Step 4: Write failing ranking and deduplication tests**

Prove complete request-title/episode-token matching, hash-first torrent
preference, seeder ordering, final size/provider/source tie-breaks, and
deduplication by protocol plus normalized info hash or source ID.

- [ ] **Step 5: Run ranking tests and verify RED**

Run: `go test ./internal/resolver -run 'Test(Rank|Deduplicate)'`

Expected: failures because ranking helpers do not exist.

- [ ] **Step 6: Implement ranking**

Use pure functions in `ranking.go`. Normalize comparison text by lowercasing,
replacing punctuation with spaces, and collapsing whitespace. Never inspect or
log release locator URLs while ranking.

- [ ] **Step 7: Run resolver race tests**

Run: `go test -race ./internal/resolver`

Expected: PASS.

- [ ] **Step 8: Commit resolver**

```bash
git add go.mod go.sum internal/resolver/service.go internal/resolver/service_test.go internal/resolver/ranking.go internal/resolver/ranking_test.go
git commit -m "feat: rank acquisition search results"
```

### Task 4: Documentation and regression verification

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/acceptance-evidence.md`

**Interfaces:**
- Consumes: completed acquisition domain, Prowlarr gateway, and resolver tests
- Produces: an evidence-backed roadmap state without claiming live Prowlarr or TorBox mutation

- [ ] **Step 1: Update architecture and status**

Document the read-only search boundary, ephemeral release locators, partial
provider failure behavior, and the still-pending TorBox cached-acquisition
state machine. Do not describe mocked Prowlarr evidence as live.

- [ ] **Step 2: Run complete verification**

Run:

```bash
make verify
make compose-check portable-compose-check rolling-compose-check torbox-compose-check
cd web && bun run lint && bun run test && bun run build
```

Expected: every command passes and project coverage remains at least 80%.

- [ ] **Step 3: Scan safety boundaries**

Run:

```bash
git diff --check
# Search tracked files for the locally known credential without recording it here.
```

Expected: no whitespace errors and no saved TorBox credential fragment in
tracked files.

- [ ] **Step 4: Commit documentation**

```bash
git add README.md docs/architecture.md docs/acceptance-evidence.md
git commit -m "docs: record acquisition search foundation"
```
