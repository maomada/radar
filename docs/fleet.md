# Fleet Manager: read-only foundation

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
| `get_fleet_overview` | Optional `fleetId`, `clusterIds` | Per-cluster resource counts, source/time and coverage; health explicitly `not_assessed` |
| `search_fleet_resources` | `resource`; optional `fleetId`, `clusterIds`, `namespace`, `nameContains`, `labelSelector`, `limit` | Metadata only: cluster/connection, group/kind/namespace/name, UID, resourceVersion, source/time and per-cluster coverage |

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

There is no Fleet UI or public membership-edit API yet; operators manage the JSON.
There are no cross-cluster relationships, Karmada discovery, health diagnosis,
logs/events/topology evidence tools, resource application, restart or scaling tools.
The model does not yet allow approving a merge of duplicate candidates. The
read-only foundation must not be described as complete Fleet Manager acceptance.
Tests using fake clients or HTTP fixtures are not real-cluster deployment evidence.
