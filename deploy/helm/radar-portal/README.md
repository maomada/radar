# Radar Portal Helm Chart

This chart deploys the independent, self-hosted Radar Portal server. The chart
does not publish or select a prebuilt image: build the Portal image from this
repository and supply an image from a registry you control. For the complete
registration model, credential handling, TLS, and Radar-side configuration,
see [the Portal deployment guide](../../../docs/portal.md).

## Prerequisites

- Kubernetes and Helm 3
- A PersistentVolume provisioner and a PVC for the registry
- An externally managed Secret containing `tokens`, `username`, and `password`
- A locally built Portal image in a registry accessible to the cluster

The `tokens` Secret key contains a JSON object mapping cluster IDs to unique
registration tokens. `username` and `password` are the separate viewer login
credentials. Each token must be at least 32 characters; the username must be
1–256 bytes and contain no colon or control characters, and the password must
be 16–4096 bytes. The server rejects duplicate
registration tokens.

Create and rotate these values through your approved secret manager or
GitOps Secret mechanism. Do not put the values into Helm values, chart files,
commands that are retained in shell history, or the Helm release state. The
chart only references the existing Secret by name.

## Build an image

Build from the repository root and push only to a registry you control:

```bash
docker build -f Dockerfile.portal -t registry.example.internal/radar-portal:dev .
docker push registry.example.internal/radar-portal:dev
```

These commands are examples; this repository does not claim that a Portal
image or chart has been published.

## Install

Supply your existing Secret and image repository:

```bash
helm upgrade --install radar-portal deploy/helm/radar-portal \
  --namespace radar-portal --create-namespace \
  --set image.repository=registry.example.internal/radar-portal \
  --set image.tag=dev \
  --set existingSecret=radar-portal-auth
```

The chart creates one `Recreate` Deployment and a required PVC for the
single-writer JSON registry. It does not create a Secret, ServiceAccount,
RBAC resources, or any Kubernetes API access. The Pod disables automatic
ServiceAccount token mounting, runs as non-root with a read-only root
filesystem, and mounts the registry PVC at `/var/lib/radar-portal`.

The Service is cluster-internal by default. If exposing it with an Ingress,
configure TLS termination at the ingress or another trusted reverse proxy.
The Portal UI, API, and assets are root-hosted: use a dedicated hostname with
path `/` and `pathType: Prefix`; subpath hosting is not supported.
The Portal's listener itself speaks HTTP; never expose viewer login
credentials or bearer registration traffic over an untrusted plaintext
network.

Visitors first see `/login`; successful login opens the protected overview.
The Portal issues `HttpOnly`, `Secure`, `SameSite=Strict` cookies, so viewer
access requires HTTPS via the ingress or trusted TLS proxy. Sessions expire
after eight hours or a Portal restart. The overview includes a logout action
that revokes the current session. Login credentials remain in the existing
Secret; no additional session secret or chart setting is required.

The PVC is the sole durable registry copy. Back it up using the storage
platform's supported process. The server is deliberately single-replica and
has no multi-writer, replication, or built-in backup mechanism.
