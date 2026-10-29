# Fleet Manager: managed connections and metadata API

Fleet is opt-in. The same Radar process connects directly to multiple Kubernetes
API servers, using a separate client, discovery client and informer cache for each
connection. The existing UI and `/mcp` still use their original single-cluster
Context; switching that Context does not retarget Fleet requests.

This increment provides a persistent identity catalog, configuration-based Fleet
membership and the independent HTTP endpoint `/mcp-fleet` (under `--base-path`
when configured). It is not the independent Radar Portal directory and does not
use Portal registration or viewer credentials.

## Local configuration

Create a trusted operator-owned JSON file:

```json
{
  "version": 1,
  "connections": [
    {
      "id": "east",
      "name": "East",
      "source": {"type": "kubeconfig", "kubeconfig": "./east.yaml", "context": "east"},
      "namespaces": ["production"]
    },
    {
      "id": "west",
      "name": "West",
      "source": {"type": "kubeconfig", "kubeconfig": "./west.yaml", "context": "west"}
    }
  ],
  "fleets": [{"id": "production", "name": "Production", "connections": ["east", "west"]}]
}
```

```bash
radar --fleet-config ./fleet.json --fleet-state ./fleet-state.json --no-browser
```

The normal Radar kubeconfig/ServiceAccount setup still applies to the original UI.
Fleet uses explicit contexts from its own connection document, not the UI's
current Context or `kubectl config use-context`. Relative kubeconfig paths resolve
against the Fleet configuration file. The internal discovery helper reads each
explicit kubeconfig separately; there is no automatic enrollment of all contexts.
Kubeconfigs are executable trusted configuration (for example, exec credential
plugins), not untrusted uploads. They are never returned over MCP or copied into
the catalog. Do not put credentials in connection names or IDs.

Authenticated operators can manage Fleet metadata only when their exact username
appears in the comma-separated `--fleet-admin-users` flag; it defaults to empty.
Kubernetes read access does not grant Fleet administration. With Radar auth
disabled, management is available only on the loopback listener.
`--fleet-local-connections` (default false) additionally enables trusted local
kubeconfig admission and removal; it is forbidden in-cluster. These controls do
not enable Kubernetes writes.

The protected registry persists stable cluster IDs and managed kubeconfig source
references, never kubeconfig contents or credentials. Candidate IDs are resolved
again from trusted server-side context discovery when admitted. The candidate API
returns only `{id,name,context,sourceLabel}` and the source label is a basename;
clients cannot submit paths, kubeconfig data, URLs or arbitrary contexts. A
managed source missing or no longer present in trusted discovery is retained with
its stable ID but becomes inactive and unroutable after restart. Re-adding the
same connection ID and source can reactivate that record. Explicit removal also
leaves the identity record inactive, so a later deliberate re-addition preserves
the ID.

Local connections inherit startup namespace settings as a hard collection clamp.
When `--namespace-scope` is set, its selected namespace remains the boundary;
managed connections cannot broaden it. The ordinary namespace picker does not
retarget Fleet.

`--fleet-state` defaults to `~/.radar/fleet-state.json`. Preserve this file across
restarts: it contains stable internal cluster IDs, identity evidence and membership,
not Kubernetes credentials. Only one process may own it. An invalid/locked catalog
fails startup rather than silently assigning new IDs. Configuration is read at
startup; restart Radar after edits, credential rotation or connection recovery.
This increment does not automatically reconnect or live-reload configuration.

An explicit `namespaces` array is a hard collection boundary for namespaced kinds.
Omitting it requests cluster-wide collection and may return denied. Cluster-scoped
Nodes/Namespaces still require their own Kubernetes permissions. The initial
supported kinds are Pods, Deployments, Services, Nodes and Namespaces.

## Identity and duplicate connections

Connection IDs identify operator configuration. Random internal cluster IDs are
persisted independently. Context names and API URLs are not physical identity.
Changing an existing connection's source reference requires a new connection ID.
Changing credentials in the referenced kubeconfig does not merge permissions.

The `kube-system` namespace UID is an observed identity signal, not a universal
proof (clusters can be cloned/restored). If it is unreadable, identity stays
unresolved and the connection retains a separate record. Matching observed UIDs
produce duplicate candidates; they do **not** merge clients, caches or credentials.
Changing a previously observed UID produces an identity conflict and disables
routing for that connection. Resolve that change operationally, rather than
discarding the state file to bypass the guard.

`list_clusters` reports startup connection/collection observations with timestamps;
they are not a continuous availability monitor. Informer readiness and identity
verification are not proof that a cluster is healthy. Live queries report their own
fresh acquisition status. Do not sum duplicate candidates as distinct clusters.

## MCP contract and access control

Connect an MCP Streamable HTTP client to `http://127.0.0.1:9280/mcp-fleet` locally
or to the authenticated deployment's equivalent URL. Fleet is HTTP-only in this
increment; legacy stdio/catalog-only modes do not become Fleet transports.

| Tool | Inputs | Result |
| --- | --- | --- |
| `list_clusters` | Optional `fleetId`, `clusterIds` | Active inventory, stable IDs, connection and identity observations, duplicate candidates and memberships |
| `get_fleet_overview` | Optional `fleetId`, `clusterIds` | Per-cluster rows add `assessmentState`, `findings`, `assessmentCoverage`, and `assessmentTruncated` to the scoped inventory |
| `search_fleet_resources` | `resource`; optional `fleetId`, `clusterIds`, `namespace`, `nameContains`, `labelSelector`, `limit` | Metadata only: cluster/connection, group/kind/namespace/name, UID, resourceVersion, source/time and per-cluster coverage |
| `get_fleet_resource` | `clusterId`, `resource`, `namespace`, `name`, optional `uid` | `{target,state,object?,limitations}`; one sanitized allowlisted resource; Secret reads are denied |
| `get_fleet_events` | `clusterId`, `resource`, `namespace`, `name`, `uid`, optional `limit` | `{target,state,events,truncated,limitations}` with UID-bound, redacted events |
| `get_fleet_logs` | `clusterId`, `namespace`, `name`, `container`, optional `tailLines`, `previous` | `{target,state,container,text,truncated,limitations}`; stale Pod UIDs suppress text. Redaction is best effort and cannot guarantee removal of arbitrary application secrets. |
| `diagnose_fleet_resource` | `clusterId`, `resource`, `namespace`, `name`, optional `uid` | `{target,state,findings,limitations}` with deterministic canonical health observations, not an LLM root-cause claim |
| `get_fleet_relationships` | Optional `fleetId`, `clusterIds` | `{relationships,coverage,truncated,limitations}`; evidence-backed Karmada identity/distribution declarations |
| `get_fleet_topology` | Optional `fleetId`, `clusterIds` | `{nodes,edges,coverage,truncated,limitations}` with typed referentially valid nodes and evidence-backed edges |

Omitting selection queries all active configured clusters. Supplying an unknown
cluster or a cluster outside the selected Fleet is an error, never a fallback to
the current Context. A failed peer does not erase results from healthy peers.
Search defaults to at most 100 matches and caps the requested limit at 200.
Collection is bounded to 500 objects per resource/cluster, four concurrent
clusters and a 20-second call budget. Continuation, namespace failures or collection
limits yield partial coverage. Partial counts are lower bounds; denied,
unavailable and out-of-scope counts are omitted, not zero. Search count coverage
is before the client-side name filter. `truncated` indicates the returned match
limit; collection incompleteness is separately reported in `coverage`.

Evidence reads allow only Pods, Deployments, StatefulSets, DaemonSets, ReplicaSets,
Services, Nodes and Namespaces; Secret reads are not supported. Resource detail
removes managed fields and the last-applied annotation and uses Radar's shared
structured and inline-secret redaction. Event reads bind to the fetched object's
UID, not its name. Pod logs require an explicit regular, init or ephemeral
container, are limited to 200 lines, 64 KiB and 20 seconds, and return no text
when the Pod UID changes during the read. Log redaction is best effort; it cannot
guarantee removal of arbitrary application secrets.

`diagnose_fleet_resource` is a deterministic, scoped observation using Radar's
canonical Pod, workload and Node lifecycle classifiers. Missing required fields
or incomplete coverage remain unknown; historical OOM evidence is not called a
current root cause. Overview assessments reuse their bounded collections and
keep collection coverage separate from assessment certainty. Neither surface
claims whole-cluster health.

Relationships are emitted only for supported Karmada APIs. Karmada `Cluster`
`spec.id` is compared only with fresh caller-authorized `kube-system` namespace
UIDs; it is a declared identity cross-check, not physical proof. Duplicate matches
remain ambiguous and custom IDs remain unresolved. ResourceBinding membership is
a declared distribution only; authorized exact source and destination GETs may
add observed endpoints, but do not prove controller ownership or successful
application. Coverage distinguishes unsupported APIs from denied reads.
Topology preserves unresolved destinations and every edge references a returned
node. Result arrays are always non-null; unavailable optional evidence is omitted
or represented by its explicit state rather than a false assertion.

Without Radar authentication, loopback callers use each connection's credentials.
The CLI refuses Fleet on a non-loopback listener without Radar authentication.
Do not expose an unauthenticated loopback process through a public proxy/tunnel.

With Radar authentication, each data query makes fresh Kubernetes LIST requests
impersonating that caller on **each target cluster**. Each connection identity must
have the necessary impersonation permissions there, and the caller must have LIST
permissions there. The service identity's informer cache is not used as a shortcut
around caller RBAC. Denied impersonation is denied data, not a fallback to service
credentials. Configured inventory and service collection status are visible to
authenticated Radar users; they do not themselves grant Kubernetes access.
Cloud/user-token forwarding is unsupported and fails closed: an incoming token is
never forwarded to arbitrary Fleet clusters. Configure authentication and trusted
proxy/OIDC boundaries as described in [authentication.md](authentication.md).

## HTTP inventory and management API

When Fleet is enabled, Radar mounts these routes under the existing authenticated
and same-origin `/api` boundary. They do not inherit legacy current-cluster
authorization or its namespace filter; live Fleet queries explicitly route to
selected runtimes and enforce the caller's access on each target cluster.

| Method and path | Purpose |
| --- | --- |
| `GET /api/fleet/capabilities` | Returns `{"enabled":true,"canManage":false,"localConnections":false,"writesEnabled":false}` with caller-specific `canManage` and configured `localConnections` values. Kubernetes writes remain disabled. |
| `POST /api/fleet/query/{tool}` | Runs the nine read tools listed above with the same bounded JSON inputs and structured results as MCP. |
| `GET /api/fleet/candidates` | Lists current trusted server-side context candidates; source paths and credentials are not returned. |
| `POST /api/fleet/connections` | Adds `{ "id": "...", "name": "...", "candidateId": "..." }` when local connections are enabled and the caller may manage Fleet. Returns `{ "ok": true, "clusterId": "..." }`. |
| `DELETE /api/fleet/connections/{id}` | Removes only a locally managed connection. Declarative connections are immutable. |
| `PUT /api/fleet/fleets/{id}` | Upserts `{ "name": "...", "clusterIds": [] }` and memberships. |
| `DELETE /api/fleet/fleets/{id}` | Deletes a locally managed Fleet. Declarative Fleets return `409`; edit the Fleet configuration instead. |
| `POST /api/fleet/clusters/{clusterId}/refresh` | Refreshes only that cluster's runtime. |
| `POST /api/fleet/duplicates` | Persists `{ "clusterId": "...", "otherClusterId": "...", "decision": "keep_separate" }` only for active, verified duplicate candidates. |

Successful metadata mutations return `{ "ok": true }`, except connection
admission, which also returns the stable `clusterId`. `list_clusters` and API
inventory include `managedConnectionIds` for active locally owned connections
and `duplicateDecisions`; both arrays are always non-null. Declarative records
are never included in `managedConnectionIds`. Duplicate decisions preserve
separate identities and never merge credentials, runtimes or access.
Candidate discovery is restricted to Fleet administrators; ordinary authenticated
readers can query capabilities and inventory but cannot inspect local candidates.

## Helm configuration

Use a locally built image containing this implementation; released upstream images
do not gain these flags from values alone. No image or chart is published by this
change. The canonical chart is `deploy/helm/radar` in this repository.

The existing chart extension points can mount the Fleet JSON as a ConfigMap and
per-cluster kubeconfigs as an operator-managed Secret, without embedding credentials
in Helm values. For example, assuming the Secret, ConfigMap, target RBAC and Radar
authentication have already been configured:

```yaml
replicaCount: 1
persistence:
  enabled: true
extraArgs:
  - --fleet-config=/etc/radar/fleet/fleet.json
  - --fleet-state=/data/fleet-state.json
extraVolumes:
  - name: fleet-config
    configMap:
      name: radar-fleet-config
  - name: fleet-kubeconfigs
    secret:
      secretName: radar-fleet-kubeconfigs
extraVolumeMounts:
  - name: fleet-config
    mountPath: /etc/radar/fleet
    readOnly: true
  - name: fleet-kubeconfigs
    mountPath: /etc/radar/fleet-credentials
    readOnly: true
```

Use absolute mounted paths in each `source.kubeconfig`. A local cluster can instead
use `"source": {"type": "in-cluster"}`, which retains the pod's ServiceAccount.
Ensure the container can read the mounted files and write the state directory.
Keep one replica and durable storage for the identity catalog; use a Recreate
rollout when sharing that catalog so a second pod does not contend for the lock.
Restart after external ConfigMap/Secret changes. The extension points do not add
an automatic checksum for those external objects. Do not enable Cloud tunneling
for this Fleet deployment.

## Deliberate limits of this increment

There is no Fleet UI yet. Resource application, restart and scaling tools are not
included. Duplicate candidates cannot be merged.
Fleet remains a read-only Kubernetes surface; metadata management does not
create or change Kubernetes resources. Tests using fake clients or HTTP fixtures
are not real-cluster deployment evidence.
