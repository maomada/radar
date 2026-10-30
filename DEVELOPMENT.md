# Development Guide

Guide for developers contributing to Radar or building custom versions.

## Prerequisites

- **Go 1.26+**
- **Node.js 24+**
- **npm**
- **kubectl** with cluster access

## Quick Start

```bash
git clone https://github.com/maomada/radar.git
cd radar

# Install dependencies
make deps

# Start development (two terminals)

# Terminal 1: Frontend with hot reload (port 9273)
make watch-frontend

# Terminal 2: Backend with hot reload (port 9280)
make watch-backend
```

Open http://localhost:9273 — the Vite dev server proxies `/api` requests to the Go backend.

## Make Commands

```bash
make deps             # Install all dependencies (Go + npm)
make build            # Build everything (frontend + embedded binary)
make frontend         # Build frontend only
make backend          # Build backend only
make test             # Run Go tests
make tsc              # TypeScript type check
make lint             # Run linter
make clean            # Clean build artifacts
make docker           # Build Docker image
make desktop-dev      # Desktop app dev mode with hot reload (Wails)
make desktop          # Build desktop binary
make desktop-package-darwin  # Package macOS .app bundle
```

## Project Structure

```
radar/
├── cmd/
│   ├── explorer/           # CLI entry point (main.go)
│   └── desktop/            # Desktop app entry point (Wails v2)
├── internal/
│   ├── app/               # Application lifecycle management
│   ├── config/            # Persistent configuration (~/.radar/config.json)
│   ├── helm/              # Helm SDK client and handlers
│   ├── images/            # Container image inspection
│   ├── k8s/               # Kubernetes client, informers, caching
│   ├── mcp/               # MCP (Model Context Protocol) server
│   ├── opencost/          # OpenCost integration (cost analysis)
│   ├── prometheus/        # Prometheus client and discovery
│   ├── server/            # HTTP server, REST API, SSE, WebSocket
│   ├── settings/          # User preferences (~/.radar/settings.json)
│   ├── static/            # Embedded frontend (built from web/)
│   ├── traffic/           # Traffic visualization (Hubble, Caretta, Istio)
│   └── ...                # errorlog, updater, version
├── pkg/
│   ├── ai/context/        # AI context minification for LLM-friendly output
│   ├── k8score/           # Shared K8s caching layer (informers, listers)
│   ├── timeline/          # Timeline event storage (memory/SQLite)
│   └── topology/          # Graph construction and relationships
├── packages/
│   └── k8s-ui/            # Shared UI package (@skyhook-io/k8s-ui)
├── web/                    # React frontend
│   ├── src/
│   │   ├── api/           # API client, React Query hooks, SSE
│   │   ├── components/    # React components (topology, resources, helm, etc.)
│   │   ├── contexts/      # React contexts (capabilities, namespace, dock)
│   │   ├── types.ts       # TypeScript type definitions
│   │   └── utils/         # Topology layout and helpers
│   └── package.json
├── deploy/                 # Helm chart, Dockerfile
├── docs/                   # User documentation
└── scripts/                # Release scripts
```

## Architecture

### Backend (Go)

```
┌─────────────────────────────────────────────────────────────────┐
│                         Go Backend                              │
│                                                                 │
│   ┌─────────────┐    ┌─────────────┐    ┌─────────────────┐   │
│   │   chi       │    │  Informers  │    │  SSE            │   │
│   │   Router    │───►│  (cached)   │───►│  Broadcaster    │   │
│   └─────────────┘    └─────────────┘    └─────────────────┘   │
│         │                   │                    │             │
│         ▼                   ▼                    ▼             │
│   REST API            K8s Watches         Real-time push      │
│   WebSocket (exec)    Resource cache      to browser          │
└─────────────────────────────────────────────────────────────────┘
```

**Key patterns:**
- **SharedInformers** — Watch-based caching, no polling. Resource changes arrive in milliseconds.
- **SSE Broadcaster** — Central hub for pushing real-time updates to all connected browsers.
- **Topology Builder** — Constructs a directed graph from cached resources on demand. Two modes: resources (hierarchy) and traffic (network flow).
- **Capabilities & RBAC** — SelfSubjectAccessReview checks at startup detect per-resource permissions. Informers are only created for accessible resource types. For namespace-scoped users (RoleBinding instead of ClusterRoleBinding), checks fall back from cluster-wide to namespace-scoped, and informers are scoped to the permitted namespace. The frontend hides features the user cannot access.

### Frontend (React + TypeScript)

```
┌─────────────────────────────────────────────────────────────────┐
│                      React Frontend                             │
│                                                                 │
│   ┌─────────────┐    ┌─────────────┐    ┌─────────────────┐   │
│   │  React      │    │  TanStack   │    │  @xyflow/react  │   │
│   │  Router     │───►│  Query      │───►│  + ELK.js       │   │
│   └─────────────┘    └─────────────┘    └─────────────────┘   │
│                             │                    │             │
│                             ▼                    ▼             │
│                      API + SSE hooks      Graph visualization  │
└─────────────────────────────────────────────────────────────────┘
```

**Key patterns:**
- **useEventSource** — SSE connection with automatic reconnection
- **React Query** — Server state management with caching and background refetching
- **CapabilitiesContext** — Fetches RBAC capabilities from `/api/capabilities` and hides unavailable features

### Tech Stack

**Backend:** Go 1.26+, client-go, chi router, gorilla/websocket, Helm SDK, Cilium/Hubble, go-containerregistry, SQLite, Wails v2, `go:embed`

**Frontend:** React 19, TypeScript, Vite, @xyflow/react + ELK.js, @xterm/xterm, Monaco Editor, TanStack React Query v5, Tailwind CSS v4 + shadcn/ui

## API Reference

See the route definitions in [`internal/server/server.go`](internal/server/server.go) for the operator-facing HTTP API. They remain the source of truth when adding or changing an endpoint.

## Adding Features

### New API Endpoint

1. Add route in `internal/server/server.go`:
   ```go
   r.Get("/api/my-endpoint", s.handleMyEndpoint)
   ```

2. Implement handler:
   ```go
   func (s *Server) handleMyEndpoint(w http.ResponseWriter, r *http.Request) {
       // ...
   }
   ```

### New Resource Type

1. Add informer in `internal/k8s/cache.go`
2. Add to topology builder in `pkg/topology/builder.go`
3. Add TypeScript type in `packages/k8s-ui/src/types/core.ts`

### New UI Component

1. Create component in `web/src/components/`
2. Add route if needed in `web/src/App.tsx`
3. Add API hooks if needed in `web/src/api/`

## Testing

```bash
# Go tests
make test

# TypeScript type check
make tsc

# Manual testing (two terminals)
make watch-backend   # Terminal 1
make watch-frontend  # Terminal 2
```

## Releasing

Distribution is intentionally manual and guarded. Main and pull-request CI
build and test; they do not publish artifacts. The complete source-SHA,
version, CI/CodeQL, dry-run, approval, and GHCR process is documented in
[`docs/ci-cd.md`](docs/ci-cd.md). Do not create release tags or dispatch a
publishing workflow as part of ordinary development.

Builds from this checkout are available with `make build VERSION=dev` and
`make portal VERSION=dev`. An owned CLI release or OCI chart should be
described as available only after its publication has been verified.

The Go module path and npm package names retain their historical compatibility
identifiers; they do not imply ownership of the original upstream project or
availability of package-manager releases.

## Code Style

- **Go:** `gofmt`, `golint`
- **TypeScript:** Prettier (`npm run format:write` in `web/`)
- **Commits:** Conventional commits preferred (`feat:`, `fix:`, `docs:`)
