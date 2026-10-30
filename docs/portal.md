# Self-hosted Radar Portal

Fork releases use the owned OCI images and charts described in
[`ci-cd.md`](ci-cd.md). Upstream installers, update feeds, and library
identities do not deliver fork releases.

Radar Portal is an optional, independent registry and viewer for multiple
Radar instances, including in-cluster installations and explicitly configured
local CLI or Desktop processes. Each eligible Radar reports its own metadata
outbound; the Portal does not connect to, scrape, proxy, or health-check those
Radars. Registration does not change a Radar listener or expose a local Radar
to the network.

The Portal and its reporter are not present in older Radar binaries. Build and
deploy a Radar binary/image that includes this feature; configuring Helm alone
cannot add it to an older image. The Portal is a separate executable and can
be deployed independently from the Radar workloads.

## How registration works

The reporter is inert by default. Local and in-cluster Radar processes opt in
through configuration; Radar Cloud is excluded. Cloud is checked before Portal
environment variables are parsed. MCP catalog-only and stdio modes do not
register. An opted-in process registers immediately and sends an update every
30 seconds. Requests have bounded timeouts and do not follow redirects. Portal
downtime is retried in the background and does not prevent Radar startup.

The Portal's `online`/`stale` state is based only on its server-side last-seen
timestamp, with a 90-second stale threshold. It indicates whether the reporter
has recently reached the Portal; it is not a health signal for the Kubernetes
cluster or Radar UI. A Radar can be healthy while stale if the Portal is
unreachable, and a recent registration does not prove Radar health.

The stable configured ID and display name identify one Radar process/instance,
not every Kubernetes context it can select. Each heartbeat also reports its
runtime mode (`local` or `in-cluster`), current kube context, and current
Kubernetes cluster name. A context switch updates that instance's existing
record; it does not change its ID, token, or configured display name. Portal
links and MCP endpoints address the Radar's currently selected context and are
not pinned to the last heartbeat snapshot.

Other metadata includes description, environment, region, public Radar URL,
Radar version, effective connection state, effective auth mode, and effective
MCP mode/endpoints. It does not include kubeconfig, Kubernetes credentials,
errors, progress messages, health-check results, or node counts. Metadata is
derived from operator configuration and Radar's effective runtime settings,
not from browser input.

## Build the Portal image

From the repository root:

```bash
docker build -f Dockerfile.portal --target full -t radar-portal:local .
```

For an owned fork image, use a version actually published by the manual
Release GHCR workflow. No image is claimed to have been published by this
documentation change.

## Deploy the Portal

Provision an existing Secret through an approved secret manager or GitOps
Secret mechanism. It must contain:

- `tokens`: a JSON object mapping each authorized cluster ID to a unique,
  randomly generated token of at least 32 characters. Example shape only:
  `{"prod-east":"<random-token>","staging-west":"<random-token>"}`
- `username`: a non-empty Portal login username without a colon or control
  characters (at most 256 bytes)
- `password`: the separate Portal viewer password (16–4096 bytes)

Use different, high-entropy registration tokens per cluster. Do not use these
placeholders as credentials. Do not store actual credentials in Helm values,
chart files, command history, or this document. The Portal rejects duplicate
tokens. The viewer username/password protect the UI and read API; registration
tokens are only accepted by the ID-scoped registration API and are never
accepted for viewer reads.

After a version is published by the manual workflow, install the owned OCI
chart and reference your existing Secret:

```bash
helm upgrade --install radar-portal oci://ghcr.io/maomada/charts/radar-portal \
  --namespace radar-portal --create-namespace \
  --version "$VERSION" \
  --set existingSecret=radar-portal-auth
```

The chart requires exactly one replica, uses a `Recreate` strategy, and always
creates a PVC for the local JSON registry. It has no RBAC or Kubernetes API
access and does not mount a ServiceAccount token. The JSON registry is
single-writer, not a distributed database; use storage-level backup and restore
procedures for the PVC. Losing the PVC loses the registry. There is no built-in
replication or backup service.

The container listener is HTTP and defaults to `127.0.0.1:9290` outside the
chart. For a cluster Deployment, the chart binds it to `0.0.0.0:9290` inside
the Pod and creates a ClusterIP Service. Configure TLS termination at an
Ingress or trusted reverse proxy before exposing it beyond a protected
network. Do not send login credentials or bearer tokens over untrusted
plaintext connections.

The Portal UI, API, and embedded assets are served at the host root. Use a
dedicated hostname with ingress path `/` and `pathType: Prefix`; the Portal
chart rejects subpath ingress. Do not configure a reverse-proxy prefix for the
Portal. Public Radar and MCP target URLs may still include path prefixes.

## Configure Radar registration

The Radar Helm chart defaults `portalRegistration.enabled` to `false`. For
each in-cluster Radar instance, set a unique stable ID and the matching
token-map entry in the Portal Secret. Configure the Radar's token in an
existing Secret; never put the token itself in Helm values:

```yaml
portalRegistration:
  enabled: true
  url: https://portal.example.com
  clusterId: prod-east
  clusterName: Production east
  description: Main production cluster
  environment: production
  region: us-east
  radarUrl: https://radar-east.example.com
  # Optional when MCP is exposed at a different public base URL.
  # mcpBaseUrl: https://radar-east.example.com/radar
  existingSecret: radar-east-portal-registration
  tokenKey: token
```

The Secret named above must have a `token` key whose value matches the
`prod-east` entry in the Portal's `tokens` map. The Portal URL must be
root-hosted and use HTTPS unless `portalRegistration.allowHTTP: true` is set
for an isolated development or trusted internal network. This exception does
not disable TLS verification; production should use a certificate trusted by the system CA
roots. The Portal URL must not contain credentials, query parameters, or
fragments. Public `radarUrl` and `mcpBaseUrl` may include a path prefix, but
must not contain credentials, query parameters, or fragments.

The reporter derives MCP mode from the effective Radar configuration:

- `full`: reports both the public MCP endpoint ending in `/mcp` and the
  read-only endpoint ending in `/mcp-readonly`.
- `read-only`: reports the public read-only endpoint ending in `/mcp-readonly`.
- `disabled`: reports no MCP endpoint.

`mcpBaseUrl` is the public base prefix used for constructing these endpoints;
when unset, it defaults to `radarUrl`. Ensure the public ingress routes those
paths to Radar. Portal registration does not relay or store credentials used
to authenticate to Radar, its MCP endpoint, or the Kubernetes API. Radar's
normal authentication configuration remains independent of the Portal's
viewer login.

### Local CLI and Desktop

Set the following environment variables in the local Radar process to opt in.
The token must match the Portal token-map entry for the stable instance ID:

```text
RADAR_PORTAL_URL=https://portal.example.com
RADAR_PORTAL_TOKEN=<instance-token>
RADAR_PORTAL_CLUSTER_ID=dev-laptop
RADAR_PORTAL_CLUSTER_NAME=Developer laptop
RADAR_PORTAL_RADAR_URL=https://radar.example.com:9280
```

These are the required Portal settings:

- `RADAR_PORTAL_URL`: root-hosted Portal URL.
- `RADAR_PORTAL_TOKEN`: the instance's bearer token.
- `RADAR_PORTAL_CLUSTER_ID`: stable ID matching the Portal token map.
- `RADAR_PORTAL_CLUSTER_NAME`: stable operator-chosen display name.
- `RADAR_PORTAL_RADAR_URL`: URL viewers' browsers can reach for this Radar.

Optional metadata and endpoint settings are `RADAR_PORTAL_DESCRIPTION`,
`RADAR_PORTAL_ENVIRONMENT`, `RADAR_PORTAL_REGION`,
`RADAR_PORTAL_MCP_BASE_URL`, and `RADAR_PORTAL_ALLOW_HTTP=true`. Use a Secret
manager or process environment injection for tokens; do not commit real tokens
or passwords to configuration files.

The Portal reporter is outbound-only and does not change the local listener.
The Portal user must be able to reach `RADAR_PORTAL_RADAR_URL` directly from
their browser. A `localhost` or `127.0.0.1` target refers to each viewer's own
machine, not necessarily the machine running Radar. Radar's CLI can use a
configured port; Desktop may fall back to a different port if its configured
or remembered port is busy. For Portal links, choose a stable configured
Desktop port and keep it available, or update the public URL when the port
changes.

Do not expose an unauthenticated local Radar on an untrusted network. If a
local instance must be reachable by remote Portal viewers, configure an
appropriate trusted network path, TLS termination, and Radar authentication
(for example, proxy or OIDC) independently of Portal viewer authentication.
The Portal does not relay Radar credentials or proxy requests to the target.

`portalRegistration.enabled` cannot be combined with `cloud.enabled`. The
Portal reporter is intentionally disabled for Radar Cloud mode.

## Access, rotation, and revocation

The Portal provides `GET /healthz`, `/login`, and its static assets without
authentication. Unauthenticated visits to `/` redirect to the standalone login
page; no cluster data is embedded in that page. After login, the browser opens
the overview at `/`. The overview and `GET /api/clusters` require a viewer
session, not HTTP Basic Authentication. Existing viewer username/password
environment variables and Secret keys are unchanged.

`POST /api/auth/login` accepts a JSON `username` and `password` and returns a
host-only, `HttpOnly`, `Secure`, `SameSite=Strict` session cookie. Passwords are
not stored in browser storage or the registry. Sessions expire after eight
hours (not sliding), are held only in process memory, and are invalidated by
server restart. `GET /api/auth/session` checks the session; the overview returns
to the login page when its directory request receives 401. `POST /api/auth/logout`
revokes the current session server-side and clears its cookie. Login and logout
require JSON and reject cross-origin browser requests. Login attempts are
limited to 20 per minute across the Portal, and active sessions are capped at
1024. Use ingress/network controls for additional abuse protection.

Secure cookies are enabled by default, including behind a TLS-terminating
proxy; the server does not trust forwarded headers to disable this protection.
For local HTTP development only, after securely provisioning the existing
`RADAR_PORTAL_USERNAME`, `RADAR_PORTAL_PASSWORD`, and `RADAR_PORTAL_TOKENS`
environment variables, run:

```bash
make portal
./radar-portal --listen 127.0.0.1:9290 --insecure-cookie
```

The `--insecure-cookie` option is rejected for non-loopback listen addresses.
Do not use it behind an externally accessible proxy or in production; the
Helm chart keeps the secure default and needs HTTPS at the ingress.

Registrations use `PUT /api/registrations/{id}` and require a
Bearer token bound to that same ID. The list is restricted to IDs currently
present in the Portal token map, and is sorted by ID. No delete API is
provided.

When rotating a registration token, update both the Radar-side Secret and
Portal token-map Secret through the secret manager, then roll out/restart the
affected Radar and Portal. The Portal rejects duplicate token values. To
revoke visibility, remove the ID from the Portal token map and restart the
Portal; this hides that record from viewer reads but does not delete the
persisted record. Re-adding the ID makes it visible again. Viewer credentials
can be rotated by updating the Secret and restarting the Portal.

Use HTTPS for viewer access and registration traffic. The Portal itself does
not terminate TLS. Keep it behind a trusted TLS
terminator, restrict network access as appropriate, and do not disable
certificate verification in the Radar reporter.
