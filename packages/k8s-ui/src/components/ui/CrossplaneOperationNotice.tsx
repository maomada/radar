import { useMemo, type ReactNode } from 'react'
import type { YamlPreviewResult } from './YamlReview'
import { parseAllDocuments } from 'yaml'
import { splitYamlDocuments } from '../../utils/yaml'
import { apiVersionToGroup } from '../../utils/navigation'
import { resourceKey } from '../../utils/resource-refs'
import { getCrossplanePauseState, isComposite, isCrossplanePaused, isManagedResource } from '../resources/resource-utils-crossplane'
import { AlertBanner } from './drawer-components'

const crossplaneFinalizers = new Set([
  'finalizer.managedresource.crossplane.io',
  'composite.apiextensions.crossplane.io',
  'finalizer.apiextensions.crossplane.io',
])

function isCrossplaneOperationResource(data: any): boolean {
  return isManagedResource(data) || isComposite(data) || getCrossplanePauseState(data) === 'acknowledged'
}

function isAnnotatedCrossplanePause(data: any): boolean {
  return isCrossplanePaused(data) && isCrossplaneOperationResource(data)
}

function resourceNames(resources: any[]): string {
  const names = resources.slice(0, 5).map(data =>
    `${data.kind ?? 'Resource'} ${data.metadata?.namespace ? `${data.metadata.namespace}/` : ''}${data.metadata?.name ?? 'Unnamed'}`,
  )
  if (resources.length > 5) names.push(`and ${resources.length - 5} more`)
  return names.join(', ')
}

export function CrossplaneDeleteNotice({ resources, force = false }: { resources: any[]; force?: boolean }) {
  const paused = resources.filter(isAnnotatedCrossplanePause)
  if (paused.length === 0) return null
  const blocked = paused.filter(data =>
    data.metadata?.finalizers?.some((finalizer: string) => crossplaneFinalizers.has(finalizer)),
  )
  const forceTargets = paused.filter(data => data.metadata?.finalizers?.length > 0)
  const alreadyDeleting = blocked.some(data => data.metadata?.deletionTimestamp)
  if (force ? forceTargets.length === 0 : blocked.length === 0) {
    return <p className="text-xs text-theme-text-secondary">
      已请求暂停以下资源的协调：{resourceNames(paused)}。暂停注解本身不会阻止 Kubernetes 删除资源；是否需要先完成清理取决于剩余 finalizer。
    </p>
  }
  return <AlertBanner
    variant="warning"
    className="mb-2"
    title={force ? '强制删除会绕过控制器清理' : alreadyDeleting ? '已在暂停协调时请求删除' : '暂停可能使删除停留在 Terminating 状态'}
    message={<>
      {force
        ? resources.length === 1
          ? <p>Reconciliation pause is requested for {resourceNames(forceTargets)}. This resource has finalizers.</p>
          : <p>Of {resources.length} selected resources, {forceTargets.length} {forceTargets.length === 1 ? 'has' : 'have'} pause requested and finalizers: {resourceNames(forceTargets)}.</p>
        : resources.length === 1
          ? <p>Pause requested on {resourceNames(blocked)}. This resource holds a Crossplane finalizer.</p>
          : <p>Of {resources.length} selected resources, {blocked.length} {blocked.length === 1 ? 'has' : 'have'} pause requested and {blocked.length === 1 ? 'holds' : 'hold'} a Crossplane finalizer: {resourceNames(blocked)}.</p>}
      {force ? <p className="mt-1">强制删除会移除 finalizer，可能留下外部或相关资源。此操作不会恢复协调，也不保证其他控制器会删除哪些资源。</p> : <p className="mt-1">{alreadyDeleting ? resources.length === 1 ? '已请求删除。' : '已请求删除部分资源。' : 'Kubernetes 可以接受删除请求。'} 暂停注解会让{blocked.length === 1 ? '此资源的控制器' : '这些资源的控制器'}延后 finalizer 清理。移除暂停标记会请求恢复协调，并可能触发待处理工作，包括按策略执行删除。</p>}
    </>}
  />
}

export function CrossplaneApplyNotice({ resources, unknownResources = [], children }: { resources: any[]; unknownResources?: any[]; children?: ReactNode }) {
  const paused = resources.filter(isAnnotatedCrossplanePause)
  if (paused.length === 0 && unknownResources.length === 0) return null
  return <AlertBanner
    variant="info"
    className="mb-2"
    title={paused.length ? 'Pause affects reconciliation' : 'Existing pause state is unknown'}
    message={<>
      {paused.length > 0 && <>
        <p>{resourceNames(paused)}.</p>
        <p className="mt-1">Kubernetes 可以接受修改。只要 <code>crossplane.io/paused</code> 仍为 <code>&quot;true&quot;</code>，{paused.length === 1 ? '此资源的控制器' : '这些资源的控制器'}就会延后协调。移除此注解会请求恢复协调，并可能触发待处理工作，包括删除。其他资源的控制器可能会继续独立运行。</p>
      </>}
      {unknownResources.length > 0 && <>
        <p className="mt-1">Live pause state is unverified for {resourceNames(unknownResources)}.</p>
        <p className="mt-1">If <code>crossplane.io/paused</code> remains <code>&quot;true&quot;</code> on an existing resource, its controller defers reconciliation. Omitting the annotation from server-side apply YAML does not guarantee its removal.</p>
      </>}
      {children}
    </>}
  />
}

function yamlObjects(yaml: string): any[] {
  try {
    return parseAllDocuments(yaml).map(document => document.errors.length ? null : document.toJS({ maxAliasCount: 100 }))
  } catch {
    return []
  }
}

export function CrossplaneYamlApplyNotice({ yaml, baselineYaml = '', baselineUnavailable = false, previewDocuments, mode = 'apply', resourceData }: {
  yaml: string
  baselineYaml?: string
  baselineUnavailable?: boolean
  previewDocuments?: YamlPreviewResult[]
  mode?: 'apply' | 'create' | 'update'
  resourceData?: unknown
}) {
  const context = useMemo(() => {
    const submitted = splitYamlDocuments(yaml).map(document => document.content)
    const original = splitYamlDocuments(baselineYaml).map(document => document.content)
    const matchesTarget = (data: any, target: any) => Boolean(data && target
      && typeof data.apiVersion === 'string' && typeof target.apiVersion === 'string'
      && resourceKey(apiVersionToGroup(data.apiVersion), data.kind, data.metadata?.namespace ?? '', data.metadata?.name)
        === resourceKey(apiVersionToGroup(target.apiVersion), target.kind, target.metadata?.namespace ?? '', target.metadata?.name))
    const pairs = (previewDocuments
      ? previewDocuments.map(preview => ({
          after: yamlObjects(preview.predictedYaml || preview.submittedYaml || submitted[preview.index] || '')[0],
          before: yamlObjects(preview.baselineYaml || original[preview.index] || '')[0],
          preview,
        }))
      : Array.from({ length: Math.max(submitted.length, original.length) }, (_, index) => ({
          after: yamlObjects(submitted[index] || '')[0],
          before: yamlObjects(original[index] || '')[0],
          preview: undefined,
        }))).map(pair => matchesTarget(pair.before, pair.after) ? pair : { ...pair, before: undefined })
    const rawSource = pairs.length === 1 && isAnnotatedCrossplanePause(resourceData) ? resourceData as any : undefined
    const matchesRaw = (data: any) => matchesTarget(data, rawSource)
    const rawPause = matchesRaw(pairs[0]?.after) ? rawSource : undefined
    const hasPause = (data: any) => isCrossplanePaused(data) && (isCrossplaneOperationResource(data) || Boolean(rawPause && matchesRaw(data)))
    const resources = pairs.flatMap(pair => {
      const source = hasPause(pair.after) ? pair.after : hasPause(pair.before) ? pair.before : undefined
      return source ? [isAnnotatedCrossplanePause(source) ? source : rawPause] : []
    })
    const unknownResources = pairs.filter(pair => mode !== 'create' && (baselineUnavailable || pair.preview?.status === 'unavailable' && !pair.preview.predictedYaml && !pair.preview.baselineYaml && !pair.before && pair.preview.action !== 'create'))
      .map(pair => pair.after).filter(data => data && (isCrossplaneOperationResource(data) || Boolean(rawPause && matchesRaw(data))))
    const kept = pairs.filter(pair => pair.preview?.predictedYaml && hasPause(pair.before) && hasPause(pair.after)).map(pair => pair.after)
    const requested = pairs.filter(pair => pair.preview?.predictedYaml && !hasPause(pair.before) && hasPause(pair.after)).map(pair => pair.after)
    const cleared = pairs.filter(pair => pair.preview?.predictedYaml && hasPause(pair.before) && !isCrossplanePaused(pair.after)).map(pair => pair.before)
    const omissionUnverified = pairs.some(pair => !pair.preview?.predictedYaml && hasPause(pair.before) && pair.after?.metadata?.annotations?.['crossplane.io/paused'] === undefined)
    return { resources, unknownResources, kept, requested, cleared, omissionUnverified }
  }, [yaml, baselineYaml, baselineUnavailable, previewDocuments, mode, resourceData])
  return <CrossplaneApplyNotice resources={context.resources} unknownResources={context.unknownResources}>
    {context.kept.length > 0 && <p className="mt-1">Server preview keeps the pause request on {resourceNames(context.kept)}.</p>}
    {context.requested.length > 0 && <p className="mt-1">Server preview requests a pause on {resourceNames(context.requested)}.</p>}
    {context.cleared.length > 0 && <p className="mt-1">Server preview clears the pause request on {resourceNames(context.cleared)}.</p>}
    {context.omissionUnverified && <p className="mt-1">Omitting the annotation from server-side apply YAML does not guarantee its removal.</p>}
  </CrossplaneApplyNotice>
}
