# CI/CD and owned GHCR distribution

Main and pull-request CI build and test only. This repository does not claim
that any image or chart has already been published. A maintainer may dispatch
**Release GHCR** from `maomada/radar` on `main` with a full lowercase 40-digit
source SHA and a strict SemVer version (no `v` prefix or build metadata).
The policy requires that SHA to be an ancestor of `main` and that the latest
main-push CI and CodeQL runs for that exact SHA both succeeded.

Run once with `publish=false` first. It builds each multi-platform image into
an OCI archive and packages both charts using matching `version`, `appVersion`,
image repositories, and image tags. The dry run proves neither GHCR
permissions nor collision checks nor a successful registry push. Review the
manifest artifact, archive checksums, build digests, chart checksums, and CI /
CodeQL links before a separate explicit dispatch with `publish=true`.

The publish job uses the `ghcr-publish` environment and `GITHUB_TOKEN` with
`packages:write`; builds have no registry login or package-write permission.
The environment name alone does **not** enforce approval. A repository
maintainer must configure required reviewers and restrict deployment branches
to `main` in repository settings before enabling publication. GHCR can
automatically associate newly created packages with this repository when the
first publication uses `GITHUB_TOKEN`. Existing packages must grant this
repository Actions access. Review the intended package visibility and access
policy before publishing; neither environment protection nor package
visibility was configured by this change.

Images:

- `ghcr.io/maomada/radar:<version>`
- `ghcr.io/maomada/radar-portal:<version>`

Helm OCI charts:

- `oci://ghcr.io/maomada/charts/radar --version <version>`
- `oci://ghcr.io/maomada/charts/radar-portal --version <version>`

For example, after a version is actually published:

```sh
helm upgrade --install radar oci://ghcr.io/maomada/charts/radar \
  --version "$VERSION" --namespace radar --create-namespace
helm upgrade --install radar-portal oci://ghcr.io/maomada/charts/radar-portal \
  --version "$VERSION" --namespace radar-portal --create-namespace \
  --set existingSecret=portal-auth
```

Use a secret manager or existing Kubernetes Secret for credentials. Do not put
registry credentials in chart values, runtime configuration, command arguments,
or this repository. The Radar chart supports an `imagePullSecrets` value. The
Portal chart does not; its Deployment uses the namespace default ServiceAccount.
For private GHCR images used by Portal, have the cluster operator provision that
ServiceAccount's `imagePullSecrets` before installation. Do not assume packages
are public.

Before any write, the workflow inspects all four versioned refs and blocks if
one exists or if absence cannot be established. Images are copied from the
exact archives built earlier; charts are pushed only after both image copies
succeed. The release manifest records source SHA, version, CI/CodeQL evidence,
actor, workflow run, archive checksums, build digests, and final registry
digests where known.

A registry can fail after some artifacts have been written. The workflow does
not delete, retry, or overwrite artifacts. If a push is partial or its result
is unknown, reconcile registry state and use a **fresh version**; do not rerun
the same version. Roll back by selecting a previously known-good image tag or
Helm revision (`helm history` / `helm rollback`) and verify the matching
manifest digest.

The upstream installers, update feeds, and library identities remain upstream;
they do not deliver fork releases. This migration does not change Go module or
npm package identities or rewrite installer/runtime updater behavior.
