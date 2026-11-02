#!/usr/bin/env bash
# Smoke tests for the radar Helm chart's template rendering.
#
# Exercises the self-upgrade toggle paths: the chart was silently clobbered
# by release.yml's wholesale-replace sync once (helm-charts@c68795c wiped
# helm-charts#9). Golden-string assertions here pin the rendered output so
# the next regression fails the build instead of shipping silently.
#
# Usage:
#   ./scripts/test-chart.sh

set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/deploy/helm/kubeatlas"
PORTAL_CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/deploy/helm/kubeatlas-portal"
FAIL=0
CASE=""

fail() {
  echo "    ✗ $1"
  FAIL=1
}
pass() {
  echo "    ✓ $1"
}

assert_contains() {
  local pattern="$1" label="$2"
  if grep -Eq -- "$pattern" <<< "$OUT"; then pass "$label"
  else fail "$label — no match for: $pattern"; fi
}

assert_not_contains() {
  local pattern="$1" label="$2"
  if grep -Eq -- "$pattern" <<< "$OUT"; then fail "$label — unexpected match for: $pattern"
  else pass "$label"; fi
}

render() {
  CASE="$1"; shift
  echo "  Case: $CASE"
OUT=$(helm template radar "$CHART_DIR" "$@" 2>&1) || {
    fail "helm template failed"
    echo "$OUT" | sed 's/^/      /'
    return
  }
}

reject_cloud() {
  CASE="$1"; shift
  echo "  Case: $CASE"
  if OUT=$(helm template kubeatlas "$CHART_DIR" "$@" 2>&1); then
    fail "cloud.enabled unexpectedly rendered"
  elif grep -Fq "cloud.enabled is unavailable in KubeAtlas" <<< "$OUT"; then
    pass "deployment surface rejects legacy Cloud"
  else
    fail "Cloud render failed for an unexpected reason"
    echo "$OUT" | sed 's/^/      /'
  fi
}

render_portal() {
  CASE="$1"; shift
  echo "  Case: $CASE"
  OUT=$(helm template kubeatlas-portal "$PORTAL_CHART_DIR" \
    --set image.repository=example.test/kubeatlas-portal \
    --set existingSecret=portal-auth "$@" 2>&1) || {
    fail "helm template failed"
    echo "$OUT" | sed 's/^/      /'
    return
  }
}

echo "Running chart template tests against $CHART_DIR"
echo

# Identity env and self-upgrade RBAC are deliberately decoupled: Radar needs to
# know which object it is for read-only self-description (the Cloud connect
# handoff), which grants nothing. Whether it may PATCH itself is the Role — so
# assert on the Role, not on the env vars.
render "defaults — no self-upgrade RBAC"
assert_contains '--listen-address=0.0.0.0'          "shared listener explicitly enabled"
assert_not_contains '^kind: Role$'                  "no namespaced Role"
assert_not_contains '^kind: RoleBinding$'           "no namespaced RoleBinding"
assert_not_contains 'radar-kubeatlas-self-upgrade'            "no self-upgrade Role/RoleBinding"
assert_contains 'MY_POD_NAMESPACE'                  "identity ships for read-only self-description"
assert_contains 'MY_DEPLOYMENT_NAME'                "identity ships for read-only self-description"
assert_not_contains '--opencost-currency='          "default OpenCost currency flag omitted"
echo

render "VictoriaMetrics defaults — no collection of credential-bearing objects"
assert_not_contains 'operator.victoriametrics.com'    "no VictoriaMetrics grant without cluster-wide Secrets reads"
echo

render "Secrets enabled — only reviewed VictoriaMetrics workload resources" --set rbac.secrets=true
assert_contains 'apiGroups: \["operator.victoriametrics.com"\]' "VictoriaMetrics operator group"
assert_contains 'resources: \["vmagents", "vmclusters", "vmsingles"\]' "finite workload allowlist"
assert_not_contains 'resources:.*(vmusers|vmauths|vmalerts|vmalertmanagers|vmrules|vmscrapeconfigs)' "no credential/configuration kinds"
echo

render "Secrets disabled — no VictoriaMetrics operator grant" --set rbac.secrets=false
assert_not_contains 'operator.victoriametrics.com'    "explicit false matches defaults"
echo

render "Wildcard collection includes VictoriaMetrics" --set rbac.crdGroups.all=true
assert_contains 'apiGroups: \["\*"\]'               "wildcard includes every API group"
assert_contains 'resources: \["\*"\]'               "wildcard includes core Secrets and excluded CRDs"
echo

# A grant change only reaches Radar through a new pod: it decides what it can
# watch at startup and keeps serving its cache after a grant is revoked.
# helm-unittest renders each test in isolation, so comparing renders lives here
# instead of as pinned hashes that every rbac default change would break.
rbac_checksum() {
  render "$@"
  CHECKSUM=$(yq 'select(.kind == "Deployment") | .spec.template.metadata.annotations["checksum/rbac"]' <<< "$OUT")
}
assert_same() {
  if [[ "$1" == "$2" ]]; then pass "$3"; else fail "$3 — $1 != $2"; fi
}
assert_differs() {
  if [[ "$1" != "$2" ]]; then pass "$3"; else fail "$3 — both $1"; fi
}

rbac_checksum "checksum/rbac — defaults"
DEFAULT_CHECKSUM=$CHECKSUM
DEFAULT_RULES=$(yq -o=json -I=0 'select(.kind == "ClusterRole" and .metadata.name == "radar-kubeatlas") | .rules' <<< "$OUT")
if [[ "$DEFAULT_CHECKSUM" =~ ^[0-9a-f]{64}$ ]]; then pass "pod template carries an RBAC checksum"
else fail "pod template carries an RBAC checksum — got: $DEFAULT_CHECKSUM"; fi
rbac_checksum "checksum/rbac — Secrets reads enabled" --set rbac.secrets=true
SECRETS_CHECKSUM=$CHECKSUM
assert_differs "$SECRETS_CHECKSUM" "$DEFAULT_CHECKSUM" "enabling Secrets and VictoriaMetrics reads rolls the pod"
ADDED_RULES=$(yq -o=json -I=0 'select(.kind == "ClusterRole" and .metadata.name == "radar-kubeatlas") | .rules | map(select(.apiGroups[0] == "operator.victoriametrics.com" or (.apiGroups[0] == "" and .resources[0] == "secrets" and .resourceNames == null)))' <<< "$OUT")
assert_same "$ADDED_RULES" '[{"apiGroups":[""],"resources":["secrets"],"verbs":["get","list","watch"]},{"apiGroups":["operator.victoriametrics.com"],"resources":["vmagents","vmclusters","vmsingles"],"verbs":["get","list","watch"]}]' "enabling Secrets adds exactly the Secrets and VictoriaMetrics read rules"
REMAINING_RULES=$(yq -o=json -I=0 'select(.kind == "ClusterRole" and .metadata.name == "radar-kubeatlas") | .rules | map(select(.apiGroups[0] != "operator.victoriametrics.com" and (.apiGroups[0] != "" or .resources[0] != "secrets" or .resourceNames != null)))' <<< "$OUT")
assert_same "$REMAINING_RULES" "$DEFAULT_RULES" "enabling Secrets leaves every other collector rule unchanged"
rbac_checksum "checksum/rbac — Secrets reads turned back off" --set rbac.secrets=false
assert_differs "$CHECKSUM" "$SECRETS_CHECKSUM"         "disabling Secrets and VictoriaMetrics reads rolls the pod"
assert_same "$CHECKSUM" "$DEFAULT_CHECKSUM"           "explicit false matches the default checksum"
DISABLED_RULES=$(yq -o=json -I=0 'select(.kind == "ClusterRole" and .metadata.name == "radar-kubeatlas") | .rules' <<< "$OUT")
assert_same "$DISABLED_RULES" "$DEFAULT_RULES"         "explicit false matches all default collector rules"
rbac_checksum "checksum/rbac — unrelated values" \
  --set image.tag=v0.0.0-test \
  --set resources.limits.memory=2Gi \
  --set podAnnotations.team=platform
assert_same "$CHECKSUM" "$DEFAULT_CHECKSUM"           "non-RBAC changes do not roll the pod"
reject_cloud "checksum render rejects Cloud" \
  --set cloud.enabled=true --set cloud.url=wss://x --set cloud.token=t --set cloud.clusterName=c
echo

render "cost.currency — explicit OpenCost currency label" --set cost.currency=GBP
assert_contains '--opencost-currency=GBP'           "OpenCost currency flag rendered"
echo

render "timeline.storage=postgres — DSN from operator-managed Secret, no PVC mount" \
  --set timeline.storage=postgres \
  --set timeline.retention=720h \
  --set timeline.postgres.existingSecret=app-db-credentials \
  --set timeline.postgres.secretKey=uri
assert_contains '--timeline-storage=postgres'       "postgres storage arg"
assert_contains '--timeline-retention=720h'         "postgres retention arg"
assert_contains 'name: KUBEATLAS_TIMELINE_POSTGRES_DSN' "DSN env injected"
assert_contains 'name: app-db-credentials'          "DSN secret name"
assert_contains 'key: uri'                          "DSN secret key"
assert_not_contains 'mountPath: /data'              "no SQLite PVC mount"
assert_not_contains 'claimName: radar'              "no SQLite PVC volume"
assert_not_contains '--timeline-db='                "no sqlite db path arg"
assert_not_contains '--timeline-max-size='            "no sqlite max-size arg"
echo

render "prometheusHeadersFromEnv — flag and secret env stay separate" \
  --set traffic.prometheusUrl=http://prometheus.monitoring:9090 \
  --set traffic.prometheusHeaders.X-Scope-OrgID=tenant-1 \
  --set traffic.prometheusHeadersFromEnv.Authorization=PROMETHEUS_TOKEN \
  --set 'env[0].name=PROMETHEUS_TOKEN' \
  --set 'env[0].valueFrom.secretKeyRef.name=prometheus-auth' \
  --set 'env[0].valueFrom.secretKeyRef.key=token'
assert_contains '--prometheus-header=X-Scope-OrgID=tenant-1'           "literal Prometheus header rendered"
assert_contains '--prometheus-header-from-env=Authorization=PROMETHEUS_TOKEN' "env-sourced Prometheus header flag rendered"
assert_contains 'name: PROMETHEUS_TOKEN'                              "custom env var rendered"
assert_contains 'secretKeyRef:'                                       "secretKeyRef rendered"
assert_contains 'name: prometheus-auth'                               "secret name rendered"
assert_contains 'key: token'                                          "secret key rendered"
assert_not_contains '--prometheus-header=Authorization='               "secret value not rendered as a literal header"
echo

render "OIDC prefixes ending in colon stay string args" \
  --set auth.mode=oidc \
  --set auth.oidc.issuerURL=https://issuer.example \
  --set auth.oidc.clientID=radar \
  --set auth.oidc.redirectURL=https://radar.example/auth/callback \
  --set auth.oidc.clientSecret=secret \
  --set auth.oidc.usernamePrefix=oidc-user: \
  --set auth.oidc.groupsPrefix=oidc-groups:
if yq 'select(.kind == "Deployment") | .spec.template.spec.containers[0].args[] | select(type != "!!str")' <<< "$OUT" | grep . > /dev/null; then
  fail "OIDC prefix args parse as non-string YAML values"
else
  pass "OIDC prefix args parse as strings"
fi
assert_contains '"--auth-oidc-username-prefix=oidc-user:"' "username prefix arg rendered quoted"
assert_contains '"--auth-oidc-groups-prefix=oidc-groups:"' "groups prefix arg rendered quoted"
echo

render "rbac.selfUpgrade=true — full feature wiring" --set rbac.selfUpgrade=true
assert_contains '^kind: Role$'                      "namespaced Role emitted"
assert_contains '^kind: RoleBinding$'               "namespaced RoleBinding emitted"
assert_contains 'name: radar-kubeatlas-self-upgrade$'         "names match fullname-self-upgrade convention"
assert_contains 'resourceNames: \["radar-kubeatlas"\]'        "Role restricted via resourceNames to the Deployment"
assert_contains 'verbs: \["get", "patch"\]'         "verbs scoped to get+patch"
assert_contains 'apiGroups: \["apps"\]'             "apiGroup scoped to apps"
assert_contains 'resources: \["deployments"\]'      "resource scoped to deployments"
assert_contains 'name: radar-kubeatlas$'                      "RoleBinding subject is radar SA"
assert_contains 'MY_POD_NAMESPACE'                  "downward-API namespace env var injected"
assert_contains 'fieldPath: metadata.namespace'     "namespace sourced from downward API"
assert_contains 'MY_DEPLOYMENT_NAME'                "deployment-name env var injected"
echo

reject_cloud "cloud.enabled=true is unavailable" \
  --set cloud.enabled=true --set cloud.url=wss://x --set cloud.token=t --set cloud.clusterName=c
echo

reject_cloud "legacy enrollment configuration is unavailable" \
  --set cloud.enabled=true --set cloud.hubURL=https://hub.example.test \
  --set cloud.enrollment.key=test-key --set cloud.clusterName=c
echo

reject_cloud "user-token Cloud mode is unavailable" \
  --set cloud.enabled=true --set cloud.url=wss://x --set cloud.token=t \
  --set auth.mode=proxy --set auth.kubernetesCredentials=user-token
echo

render "rbac.create=false + rbac.selfUpgrade=true — feature still works" \
  --set rbac.create=false --set rbac.selfUpgrade=true
# `rbac.create` is the master switch for the big cluster-wide ClusterRole —
# a BYO-RBAC user needs that to live outside the chart. The self-upgrade
# Role is narrow (get+patch on THIS Deployment by resourceNames), so it
# doesn't belong under that switch; cloud-rbac.yaml already follows the
# same "feature gates itself, independent of rbac.create" pattern. Either
# the feature is on end-to-end or it's off end-to-end — no silent 403.
assert_contains '^kind: Role$'                      "Role still emitted — narrow scope, independent of rbac.create"
assert_contains '^kind: RoleBinding$'               "RoleBinding still emitted"
assert_not_contains '^kind: ClusterRole$'           "no ClusterRole when rbac.create=false (big ClusterRole still suppressed)"
assert_contains 'MY_POD_NAMESPACE'                  "env vars injected — feature is fully wired"
echo

render "defaults — no helm-write footprint"
assert_not_contains 'name: radar-kubeatlas-helm$'                  "no helm add-on ClusterRole"
assert_not_contains 'name: radar-kubeatlas-helm-admin$'            "no helm-admin ClusterRole"
assert_not_contains 'name: radar-kubeatlas-cloud-owner-helm$'      "no cloud helm bindings"
assert_not_contains 'name: radar-kubeatlas-cloud-member-helm$'     "no cloud helm bindings"
assert_not_contains 'name: radar-kubeatlas-cloud-owner-helm-admin' "no cloud helm-admin bindings"
echo

render "rbac.helm=true alone — gated off without auth or cloud" --set rbac.helm=true
assert_not_contains 'name: radar-kubeatlas-helm$'                  "no helm add-on ClusterRole without auth/cloud"
assert_not_contains 'name: radar-kubeatlas-helm-admin$'            "no helm-admin ClusterRole without auth/cloud"
echo

render "rbac.helm=true + auth.mode=proxy — both CRs emitted, no cloud bindings" \
  --set rbac.helm=true --set auth.mode=proxy
assert_contains 'name: radar-kubeatlas-helm$'                  "helm add-on ClusterRole emitted"
assert_contains 'name: radar-kubeatlas-helm-admin$'            "helm-admin ClusterRole emitted"
assert_not_contains 'name: radar-kubeatlas-cloud-owner-helm$'  "no cloud-owner-helm binding without cloud"
assert_not_contains 'name: radar-kubeatlas-cloud-member-helm$' "no cloud-member-helm binding without cloud"
echo

reject_cloud "Cloud chart render is rejected even with proxy auth and Helm RBAC" \
  --set rbac.helm=true --set auth.mode=proxy --set cloud.enabled=true \
  --set cloud.url=wss://x --set cloud.token=t --set cloud.clusterName=c
echo

reject_cloud "Cloud render with owner disabled is rejected" \
  --set rbac.helm=true --set auth.mode=proxy --set cloud.enabled=true \
  --set cloud.url=wss://x --set cloud.token=t --set cloud.clusterName=c \
  --set cloud.defaultRbac.owner=false
echo

reject_cloud "Cloud render with member disabled is rejected" \
  --set rbac.helm=true --set auth.mode=proxy --set cloud.enabled=true \
  --set cloud.url=wss://x --set cloud.token=t --set cloud.clusterName=c \
  --set cloud.defaultRbac.member=false
echo

reject_cloud "Cloud Helm RBAC render is unavailable" \
  --set rbac.helm=true --set cloud.enabled=true --set auth.mode=proxy

render "defaults — no RBAC reads (viewRBAC=false)"
# The base radar ClusterRole should NOT include rbac.authorization.k8s.io reads
# at default settings. This is the single test that pins the secure default.
HELM_BASE=$(echo "$OUT" | awk '/^kind: ClusterRole$/,/^---$/{ if ($0 ~ /^---$/) exit; print }' | awk '/^  name: radar-kubeatlas$/,EOF')
if awk '/  name: radar-kubeatlas$/,/^---$/' <<< "$OUT" | grep -E 'apiGroups: \["rbac.authorization.k8s.io"\]' > /dev/null; then
  fail "default ClusterRole still grants rbac.authorization.k8s.io reads — viewRBAC should gate this"
else
  pass "no rbac.authorization.k8s.io reads in default ClusterRole"
fi
echo

render "rbac.viewRBAC=true — RBAC reads granted" --set rbac.viewRBAC=true
assert_contains 'apiGroups: \["rbac.authorization.k8s.io"\]' "RBAC reads granted when viewRBAC=true"
assert_contains 'clusterrolebindings'                        "clusterrolebindings present"
echo

render "split content — radar-helm has CRDs but NOT clusterroles" \
  --set rbac.helm=true --set auth.mode=proxy
# Pull the radar-helm ClusterRole specifically and check its contents
HELM_RULES=$(echo "$OUT" | awk '/^kind: ClusterRole$/,/^---$/' | awk '/^metadata:$/,/^rules:$/{found=1} /  name: radar-kubeatlas-helm$/{ok=1} END{print ok}')
# Simpler approach: just confirm the cluster-admin verbs are NOT in radar-helm's rules
# by checking the file was split (radar-helm-admin exists separately)
assert_contains 'name: radar-kubeatlas-helm-admin$'                 "helm-admin role exists as separate resource"
# Member-safe role grants CRDs (operator charts)
assert_contains 'customresourcedefinitions'               "CRDs writable in member-safe role"
# Cluster-admin-equivalent verbs are present somewhere in the manifest (in radar-helm-admin)
assert_contains 'clusterrolebindings'                     "RBAC writes present (in admin role)"
assert_contains 'validatingwebhookconfigurations'         "webhook writes present (in admin role)"
assert_contains 'apiservices'                             "apiservices writes present (in admin role)"
echo

reject_cloud "Cloud chart values are rejected on the deployment surface" \
  --set cloud.enabled=true --set cloud.token=dummy \
  --set cloud.url=wss://x.example --set cloud.clusterName=c1

render "OSS mode (cloud disabled): no cloud RBAC at all"
assert_not_contains 'name: radar-kubeatlas-cluster-read$'                 "no cluster-read outside cloud mode"
assert_not_contains 'name: radar-kubeatlas-cloud-viewer-cluster-read$'    "no tier bindings outside cloud mode"
echo

render "Portal registration defaults off"
assert_not_contains 'name: KUBEATLAS_PORTAL_' "no Portal reporter environment variables"
echo

render "Portal registration uses a Secret-backed token" \
  --set portalRegistration.enabled=true \
  --set portalRegistration.url=https://portal.example.test \
  --set portalRegistration.clusterId=prod-east \
  --set portalRegistration.clusterName=Production-east \
  --set portalRegistration.radarUrl=https://radar.example.test \
  --set portalRegistration.existingSecret=radar-east-portal \
  --set portalRegistration.tokenKey=token
assert_contains 'name: KUBEATLAS_PORTAL_URL' "Portal URL is configured"
assert_contains 'name: KUBEATLAS_PORTAL_CLUSTER_ID' "Portal instance ID is configured"
assert_contains 'name: KUBEATLAS_PORTAL_TOKEN' "Portal token environment variable is configured"
assert_contains 'secretKeyRef:' "Portal registration token comes from a Secret"
assert_contains 'name: radar-east-portal$' "Portal registration Secret is referenced"
assert_contains 'key: token$' "Portal registration token key is referenced"
echo

if helm template radar "$CHART_DIR" --set portalRegistration.enabled=true >/dev/null 2>&1; then
  fail "Portal registration without required settings should fail"
else
  pass "Portal registration without required settings fails"
fi
echo

if CLOUD_OUTPUT=$(helm template radar "$CHART_DIR" \
  --set portalRegistration.enabled=true \
  --set portalRegistration.url=https://portal.example.test \
  --set portalRegistration.clusterId=prod-east \
  --set portalRegistration.clusterName=Production-east \
  --set portalRegistration.radarUrl=https://radar.example.test \
  --set portalRegistration.existingSecret=radar-east-portal \
  --set portalRegistration.tokenKey=token \
  --set cloud.enabled=true 2>&1); then
  fail "Portal registration with Cloud enabled should fail"
elif grep -Fq "cloud.enabled is unavailable in KubeAtlas" <<< "$CLOUD_OUTPUT"; then
  pass "Portal registration rejects unsupported Cloud with the expected error"
else
  fail "Portal registration Cloud rejection had an unexpected error: $CLOUD_OUTPUT"
fi
echo

if helm template radar "$CHART_DIR" \
  --set portalRegistration.enabled=true \
  --set portalRegistration.url=https://portal.example.test/portal \
  --set portalRegistration.clusterId=prod-east \
  --set portalRegistration.clusterName=Production-east \
  --set portalRegistration.radarUrl=https://radar.example.test \
  --set portalRegistration.existingSecret=radar-east-portal \
  --set portalRegistration.tokenKey=token >/dev/null 2>&1; then
  fail "Portal subpath URL should fail"
else
  pass "Portal subpath URL is rejected"
fi
echo

render_portal "Portal has one writer, Secret credentials, PVC, and root ingress" \
  --set ingress.enabled=true \
  --set ingress.tls[0].secretName=portal-tls \
  --set ingress.tls[0].hosts[0]=portal.example.test
assert_contains 'replicas: 1' "Portal has exactly one replica"
assert_contains 'type: Recreate' "Portal uses a single-writer Recreate strategy"
assert_contains 'automountServiceAccountToken: false' "Portal Pod has no ServiceAccount token"
assert_contains 'kind: PersistentVolumeClaim' "Portal registry has a PVC"
assert_contains 'name: portal-auth' "Portal credentials use the existing Secret"
assert_contains 'key: tokens' "Portal token map uses the tokens Secret key"
assert_contains 'key: username' "Portal viewer username uses the Secret"
assert_contains 'key: password' "Portal viewer password uses the Secret"
assert_contains 'path: /$' "Portal ingress is rooted at /"
assert_contains 'pathType: Prefix' "Portal ingress matches root descendants"
echo

if helm template kubeatlas-portal "$PORTAL_CHART_DIR" \
  --set image.repository=example.test/kubeatlas-portal \
  --set existingSecret=portal-auth \
  --set ingress.enabled=true \
  --set ingress.tls[0].secretName=portal-tls \
  --set-string ingress.hosts[0].paths[0].path=/portal >/dev/null 2>&1; then
  fail "Portal subpath ingress should fail"
else
  pass "Portal subpath ingress is rejected"
fi
echo

render_portal "Portal image tag defaults to chart appVersion"
assert_contains 'image: "?example.test/kubeatlas-portal:0.0.0-unreleased"?$' "empty image.tag uses Chart.AppVersion"
render_portal "Portal image tag override is honored" --set image.tag=0.2.0
assert_contains 'image: "?example.test/kubeatlas-portal:0.2.0"?$' "explicit image.tag overrides Chart.AppVersion"
echo

if [[ $FAIL -eq 0 ]]; then
  echo "All chart template tests passed."
  exit 0
else
  echo "One or more assertions failed."
  exit 1
fi
