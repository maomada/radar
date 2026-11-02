package mcp

import (
	"context"
	"time"

	"github.com/maomada/radar/internal/issues"
	"github.com/maomada/radar/internal/k8s"
	"github.com/maomada/radar/internal/meaningfulchanges"
	pkgauth "github.com/maomada/radar/pkg/auth"
	"github.com/maomada/radar/pkg/issuesapi"
	"github.com/maomada/radar/pkg/resourcecontext"
	"k8s.io/apimachinery/pkg/runtime"
)

func mcpTemplateDivergence(ctx context.Context, obj runtime.Object) *resourcecontext.TemplateDivergence {
	return issues.NewCacheProvider().TemplateDivergence(obj, issueClusterScopedAccess(ctx), issueRelatedResourceAccess(ctx), func(verb, group, resource, namespace string) bool {
		return canReadInNamespace(ctx, group, resource, namespace, verb)
	})
}

func mcpClusterScopedChanges(ctx context.Context, namespace string, since time.Duration, quietStartup bool) *issuesapi.ClusterScopedChanges {
	nsLabels := meaningfulchanges.ReadNamespaceLabels(namespace, issueRelatedResourceAccess(ctx)(issues.Ref{Kind: "Namespace", Name: namespace}))
	return meaningfulchanges.ClusterScopedForNamespace(ctx, namespace, nsLabels, since, func(kind string) bool {
		if pkgauth.UserFromContext(ctx) == nil {
			return true
		}
		return k8s.ChangeReadAllowed(kind, "admissionregistration.k8s.io/v1", "", mcpChangeAuthorizer(ctx))
	}, quietStartup)
}
