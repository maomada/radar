import { useState, useEffect, useRef } from 'react'
import { Loader2, Lock, ExternalLink } from 'lucide-react'
import { clsx } from 'clsx'
import { useQuery } from '@tanstack/react-query'
import {
  rbacVerbBadgeClass,
  rbacResourceBadgeClass,
  rbacApiGroupBadgeClass,
  rbacResourceNameBadgeClass,
  rbacNonResourceUrlBadgeClass,
  type RBACWhoamiResponse,
} from '@maomada/k8s-ui'
import { useNamespaces, useAuthMe, fetchJSON } from '../../api/client'
import { useRBACWhoami } from '../../api/rbac'

// MyPermissionsContent renders the "your access on this cluster" viewer inline
// (no modal chrome) so the Settings dialog can host it directly in its content
// pane. `active` gates the apiserver queries so opening Settings on another
// section doesn't fire a SelfSubjectRulesReview the user never asked for.
export function MyPermissionsContent({ active }: { active: boolean }) {
  const [namespace, setNamespace] = useState('default')
  const { data: authMe } = useAuthMe()
  const { data: namespaces } = useNamespaces()
  const { data: whoami, isLoading, error } = useRBACWhoami(namespace, active)

  // Land on an accessible namespace: once the list loads, if the current pick
  // ('default' initially) isn't among the user's namespaces and they haven't
  // chosen one, switch to the first accessible one — otherwise a user scoped to
  // e.g. team-a would open on an empty 'default'.
  const userPicked = useRef(false)
  useEffect(() => {
    if (userPicked.current || !namespaces?.length) return
    if (!namespaces.some((ns) => ns.name === namespace)) {
      setNamespace(namespaces[0].name)
    }
  }, [namespaces, namespace])

  return (
    <div className="space-y-4">
      {/* Identity + namespace selector */}
      <div className="flex flex-wrap items-end gap-3">
        <div className="flex-1 min-w-[180px]">
          <label className="block text-xs text-theme-text-tertiary mb-1">身份</label>
          <div className="px-2 py-1.5 text-sm bg-theme-elevated rounded border border-theme-border text-theme-text-primary truncate">
            {authMe?.username || '（当前 kubeconfig 用户）'}
          </div>
        </div>
        <div className="flex-1 min-w-[180px]">
          <label className="block text-xs text-theme-text-tertiary mb-1">命名空间</label>
          <select
            value={namespace}
            onChange={(e) => { userPicked.current = true; setNamespace(e.target.value) }}
            className="w-full px-2 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded text-theme-text-primary focus:outline-none focus:border-blue-500"
          >
            {namespaces?.map((ns) => (
              <option key={ns.name} value={ns.name}>{ns.name}</option>
            ))}
            {!namespaces?.some((ns) => ns.name === namespace) && (
              <option value={namespace}>{namespace}</option>
            )}
          </select>
        </div>
      </div>

      <p className="text-xs text-theme-text-tertiary">
        由 Kubernetes API 通过{' '}
        <code className="inline-code">SelfSubjectRulesReview</code>.
        显示你在 <span className="text-theme-text-secondary">{namespace}</span> 中可以执行的操作，
        以及适用于所有位置的集群范围规则。
      </p>

      {whoami?.incomplete && (
        <div className="px-2.5 py-1.5 text-xs bg-amber-500/10 border border-amber-500/20 rounded">
          apiserver 返回的规则集不完整
          {whoami.evaluationError ? ` (${whoami.evaluationError})` : ''}.
          下面的列表可能低估你的实际权限。
        </div>
      )}

      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-theme-text-tertiary">
          <Loader2 className="w-3.5 h-3.5 animate-spin" />
          正在查询 apiserver…
        </div>
      ) : error ? (
        <div className="text-sm text-red-400">
          加载失败：{(error as Error).message}
        </div>
      ) : whoami ? (
        <PermissionsTable whoami={whoami} />
      ) : null}

      <RestrictedResources enabled={active} />
    </div>
  )
}

function PermissionsTable({ whoami }: { whoami: RBACWhoamiResponse }) {
  const resourceRules = whoami.resourceRules ?? []
  const nonResourceRules = whoami.nonResourceRules ?? []

  return (
    <div className="space-y-4">
      <div>
        <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-2">
          资源权限（{resourceRules.length}）
        </div>
        {resourceRules.length === 0 ? (
          <div className="text-sm text-theme-text-tertiary">
            此命名空间中没有资源权限。
          </div>
        ) : (
          <div className="space-y-2">
            {resourceRules.map((r, i) => (
              <ResourceRuleRow key={i} rule={r} />
            ))}
          </div>
        )}
      </div>

      {nonResourceRules.length > 0 && (
        <div>
          <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-2">
            非资源权限（{nonResourceRules.length}）
          </div>
          <div className="space-y-1 text-xs">
            {nonResourceRules.map((r, i) => (
              <div key={i} className="flex items-center gap-1 flex-wrap">
                {(r.verbs ?? []).map((v: string) => (
                  <span key={v} className={clsx('badge', rbacVerbBadgeClass(v))}>{v}</span>
                ))}
                <span className="text-theme-text-secondary">作用于</span>
                {(r.nonResourceURLs ?? []).map((u: string) => (
                  <span key={u} className={clsx('badge', rbacNonResourceUrlBadgeClass)}>{u}</span>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function ResourceRuleRow({ rule }: { rule: { verbs?: string[]; apiGroups?: string[]; resources?: string[]; resourceNames?: string[] } }) {
  const verbs = rule.verbs ?? []
  const resources = rule.resources ?? []
  const groups = rule.apiGroups ?? []
  const resourceNames = rule.resourceNames ?? []
  return (
    <div className="card-inner text-xs flex items-center gap-1 flex-wrap">
      {verbs.map((v) => (
        <span key={v} className={clsx('badge', rbacVerbBadgeClass(v))}>{v}</span>
      ))}
      <span className="text-theme-text-secondary">作用于</span>
      {resources.length === 0 ? (
        <span className="text-theme-text-secondary italic">（无资源）</span>
      ) : (
        resources.map((r) => (
          <span key={r} className={clsx('badge', rbacResourceBadgeClass)}>
            {r === '*' ? '*' : r}
          </span>
        ))
      )}
      {groups.length > 0 && groups.some((g) => g !== '') && (
        <>
          <span className="text-theme-text-secondary">位于</span>
          {groups.map((g) => (
            <span key={g} className={clsx('badge', rbacApiGroupBadgeClass)}>
              {g === '' ? 'core' : g}
            </span>
          ))}
        </>
      )}
      {resourceNames.length > 0 && (
        <>
          <span className="text-theme-text-secondary">名称为</span>
          {resourceNames.map((n) => (
            <span key={n} className={clsx('badge', rbacResourceNameBadgeClass)}>{n}</span>
          ))}
        </>
      )}
    </div>
  )
}

// displayKind strips the API group from a resource-counts key ("group/Kind" →
// "Kind"; core kinds have no prefix).
function displayKind(countKey: string): string {
  const i = countKey.indexOf('/')
  return i === -1 ? countKey : countKey.slice(i + 1)
}

// RestrictedResources surfaces the kinds KubeAtlas isn't showing the user (the
// resource-counts `forbidden` set) — the "what's hidden from me" half of access,
// alongside the SelfSubjectRulesReview rules above. Each forbidden kind carries
// a reason, so only the caller's own RBAC denials are listed. A KubeAtlas that
// predates reasons mixes those with not-installed/not-watched kinds, so its
// copy says "usually RBAC" rather than asserting a cause. Both link to the docs
// that carry the unblock RBAC.
function RestrictedResources({ enabled }: { enabled: boolean }) {
  const { data } = useQuery<{ forbidden?: string[]; reasons?: Record<string, string> }>({
    queryKey: ['resource-counts', 'your-access'],
    queryFn: () => fetchJSON('/resource-counts'),
    enabled,
    staleTime: 10000,
  })
  const reasons = data?.reasons
  const forbidden = (data?.forbidden ?? []).filter(key => !reasons || reasons[key] === 'rbac_denied')
  if (forbidden.length === 0) return null

  return (
    <div>
      <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-2 flex items-center gap-1.5">
        <Lock className="w-3.5 h-3.5 text-amber-400" />
        {reasons ? '受限资源' : '受限或不可用'}（{forbidden.length}）
      </div>
      <p className="text-xs text-theme-text-tertiary mb-2">
        {reasons
          ? '这些资源类型因你的权限而隐藏。'
          : 'KubeAtlas 未显示这些资源类型——通常是因为你的 RBAC 不允许列出它们，有时是因为该类型未在此集群中安装或监视。无论哪种情况，都不代表集群为空。'}
      </p>
      <div className="flex flex-wrap gap-1.5">
        {forbidden.map((k) => (
          <span
            key={k}
            className="inline-flex items-center px-2 py-0.5 text-xs rounded border border-theme-border bg-theme-elevated text-theme-text-secondary"
          >
            {displayKind(k)}
          </span>
        ))}
      </div>
      <a
        href="https://github.com/maomada/radar/blob/main/docs/cloud-rbac-baseline.md"
        target="_blank"
        rel="noreferrer"
        className="inline-flex items-center gap-1 text-xs text-accent-text hover:underline mt-2"
      >
        如何获取访问权限
        <ExternalLink className="w-3 h-3" />
      </a>
    </div>
  )
}
