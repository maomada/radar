# Security Policy

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Use [GitHub's private vulnerability reporting](https://github.com/maomada/radar/security/advisories/new) when the maintainer has enabled it. If unavailable, ask the maintainer to establish a private reporting channel before sharing any vulnerability details. Do not post sensitive details in public issues or send reports about this derivative to upstream maintainers.

Include as much of the following information as possible:

- Type of issue (e.g., privilege escalation, information disclosure)
- Full paths of source file(s) related to the issue
- Location of the affected source code (tag/branch/commit or direct URL)
- Step-by-step instructions to reproduce the issue
- Proof-of-concept or exploit code (if possible)
- Impact of the issue, including how an attacker might exploit it

No response or fix timeline is guaranteed. Release support policy will be documented when releases are published.

## Security Model

### Local Execution (Default)

When running KubeAtlas locally on your machine:

- **Uses your kubeconfig**: KubeAtlas authenticates using your existing `~/.kube/config` credentials
- **Your permissions apply**: All operations are subject to your Kubernetes RBAC permissions
- **No maintainer data upload**: KubeAtlas does not send cluster data to this repository's maintainers. Integrations you configure, including AI providers, may receive data required for their operation.
- **Usage stats are disabled**: see the [usage reporting policy](docs/usage-stats.md); old consent and environment settings cannot enable upstream telemetry
- **No cloud dependency**: Local mode does not require an account, agent, or cloud backend
- **Local storage**: Configuration persists under `~/.kubeatlas`; optional SQLite timeline storage is local-only. Review local files before sharing them.

### In-Cluster Deployment

When deploying KubeAtlas inside a Kubernetes cluster:

- **ServiceAccount-based auth**: Uses the pod's ServiceAccount for Kubernetes API access
- **RBAC-scoped permissions**: Configure the ServiceAccount with minimal required permissions
- **Team access**: Expose via Ingress with authentication (proxy or OIDC mode)
- **Per-user authorization**: Supports K8s impersonation so each user's actions are governed by their own RBAC bindings. See the [Authentication Guide](docs/authentication.md) for details.

### Capabilities

KubeAtlas provides both read and write operations:

**Read Operations:**
- Browse all Kubernetes resources (Pods, Deployments, Services, etc.)
- View resource YAML manifests and relationships
- Stream pod logs
- View Kubernetes events and resource change history
- List Helm releases and their configurations

**Write Operations:**
- Edit and update resource manifests (YAML)
- Delete resources
- Restart workloads (Deployments, StatefulSets, DaemonSets)
- Trigger, suspend, and resume CronJobs
- Exec into pod containers (terminal access)
- Create and manage port forwards
- Helm operations: upgrade, rollback, uninstall releases
- Switch kubectl contexts

All write operations require appropriate RBAC permissions. If your kubeconfig or ServiceAccount lacks permission for an operation, it will fail with an authorization error.

## Best Practices

### For Local Use

1. Use a kubeconfig with the minimum permissions you need
2. Consider using read-only ServiceAccounts for browsing production clusters
3. Don't expose the KubeAtlas port to the network when running locally
4. Keep KubeAtlas updated to the latest version

### For In-Cluster Deployment

1. Create a dedicated ServiceAccount with scoped RBAC permissions
2. Use read-only ClusterRole bindings for view-only deployments
3. Always deploy behind authentication (OAuth2 Proxy, Ingress auth, etc.)
4. Use NetworkPolicies to restrict access to the KubeAtlas pod
5. Regularly audit ServiceAccount permissions
