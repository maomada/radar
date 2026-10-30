import { useState, useMemo } from 'react'
import { Loader2 } from 'lucide-react'
import { Collapse, CollapseChevron, useDisclosure } from './Collapse'
import { ConfirmDialog } from './ConfirmDialog'
import { AlertBanner } from './drawer-components'
import { CrossplaneDeleteNotice } from './CrossplaneOperationNotice'

export interface CascadeDependent {
  kind: string
  namespace: string
  name: string
  group?: string
}

/** Cached deletion evidence; all lists are observations, not completion guarantees. */
export interface CascadeDetail {
  propagation?: string
  coverage?: { complete: boolean; narrowed?: boolean; truncated?: boolean; notChecked?: unknown[] }
  unavailableReason?: string
  notEnumerated?: string[]
  /** "ownerReferences" when dependents are garbage-collection results; absent from older servers. */
  basis?: string
  /** Deleted unless another owner Radar can't see is still live. */
  possibleDependents?: CascadeDependent[]
  /** What Argo CD or Flux deletes through its finalizer. */
  controllerTeardown?: {
    controller: string
    action: 'prune' | 'uninstall' | string
    destination: 'local' | 'remote' | 'unknown'
    resources?: CascadeDependent[]
    /** Already being deleted: the teardown may be underway. */
    terminating?: boolean
  }
}

interface ForceDeleteConfirmDialogProps {
  open: boolean
  onClose: () => void
  onConfirm: (force: boolean) => void
  resourceName: string
  resourceKind: string
  namespaceName: string
  isLoading: boolean
  cascadeDependents?: CascadeDependent[]
  cascadeLoading?: boolean
  cascadeRootResolved?: boolean
  cascadePreviewFailed?: boolean
  cascadeDetail?: CascadeDetail
  resourceData?: unknown
}

export function ForceDeleteConfirmDialog({
  open,
  onClose,
  onConfirm,
  resourceName,
  resourceKind,
  namespaceName,
  isLoading,
  cascadeDependents,
  cascadeLoading,
  cascadeRootResolved,
  cascadeDetail,
  cascadePreviewFailed,
  resourceData,
}: ForceDeleteConfirmDialogProps) {
  const [forceDelete, setForceDelete] = useState(false)
  const orphaning = cascadeDetail?.propagation === 'Orphan' && !forceDelete
  const isNamespace = resourceKind.toLowerCase() === 'namespace'
  const isCRD = ['customresourcedefinition', 'customresourcedefinitions'].includes(resourceKind.toLowerCase())

  function handleClose() {
    onClose()
    setForceDelete(false)
  }

  function handleConfirm() {
    onConfirm(forceDelete)
  }

  return (
    <ConfirmDialog
      open={open}
      onClose={handleClose}
      onConfirm={handleConfirm}
      title="删除资源"
      message={`确定要删除“${resourceName}”吗？`}
      details={isNamespace ? `这将永久删除命名空间“${resourceName}”及其中的所有资源。` : isCRD ? '这将删除 CRD 及其在整个集群中的所有实例，包括此处未显示的实例。' : namespaceName
        ? `这将从命名空间“${namespaceName}”中永久删除 ${resourceKind}“${resourceName}”。`
        : `这将永久删除集群级 ${resourceKind}“${resourceName}”。`}
      confirmLabel={forceDelete ? '强制删除' : '删除'}
      variant="danger"
      isLoading={isLoading}
      confirmDisabled={cascadeLoading}
    >
      <div className="flex flex-col gap-3 pb-1">
        {cascadeLoading && (
          <div className="flex items-center gap-2 text-xs text-theme-text-tertiary">
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            正在检查从属资源…
          </div>
        )}

        {!cascadeLoading && !cascadePreviewFailed && !orphaning && cascadeDependents && cascadeDependents.length > 0 && (
          cascadeDetail?.basis === 'ownerReferences' ? (
            <CascadeDependentsList
              dependents={cascadeDependents}
              title={`垃圾回收预计将删除 ${cascadeDependents.length} 个从属资源`}
              note="Kubernetes 会通过所有者引用删除这些资源。Finalizer 可能延迟删除。这只是当前缓存中的观察结果，并不保证删除一定完成。"
            />
          ) : (
            <CascadeDependentsList
              dependents={cascadeDependents}
              title={`也可能删除 ${cascadeDependents.length} 个相关资源`}
              note="此版本 Radar 无法区分所有权关系和其他关联。Kubernetes 只会删除将此资源列为所有者的对象。"
            />
          )
        )}

        {!cascadeLoading && !cascadePreviewFailed && !orphaning && cascadeDetail?.possibleDependents && cascadeDetail.possibleDependents.length > 0 && (
          <CascadeDependentsList
            dependents={cascadeDetail.possibleDependents}
            title={`也可能删除 ${cascadeDetail.possibleDependents.length} 个资源`}
            note="无法解析另一个所有者或所有者链。只有不存在有效所有者时，Kubernetes 才会删除这些资源。"
          />
        )}

        {!cascadeLoading && !cascadePreviewFailed && cascadeDetail?.coverage && !orphaning && !cascadeDetail.coverage.complete ? (
          <p className="text-xs text-theme-text-secondary">此预览不完整：Radar 可能未收集所有从属资源，或你无权查看它们。还可能有其他资源被删除。</p>
        ) : null}
        {!cascadeLoading && !cascadePreviewFailed && cascadeDetail?.controllerTeardown && (
          <ControllerTeardownNotice teardown={cascadeDetail.controllerTeardown} force={forceDelete} />
        )}

        {!cascadeLoading && !cascadePreviewFailed && cascadeRootResolved === false && (
          <AlertBanner
            variant="warning"
            title="尚未验证从属资源"
            message="Radar 无法验证 Kubernetes 还会删除哪些从属资源。可能还有未显示的资源会被删除。"
            className="mb-0"
          />
        )}

        {!cascadeLoading && !cascadePreviewFailed && orphaning && <p className="text-xs text-theme-text-secondary">保留待处理的 orphan 策略：所有者引用的从属资源将被孤立。控制器清理仍可能删除资源。</p>}
        {!cascadeLoading && !cascadePreviewFailed && forceDelete && cascadeDetail?.propagation === 'Orphan' && <p className="text-xs text-warning-text">强制删除会移除 orphan finalizer 并使用 Background 传播策略。所有者引用的从属资源可能因此被垃圾回收。</p>}
        {!cascadeLoading && !cascadePreviewFailed && !orphaning && cascadeDetail?.coverage?.complete && cascadeDetail.coverage.narrowed && <p className="text-xs text-theme-text-secondary">仅列出 Radar 可观察且你有权查看的资源。</p>}
        {!cascadeLoading && cascadePreviewFailed && <AlertBanner variant="warning" title="删除预览不可用" message="还可能有其他资源被删除。" className="mb-0" />}
        {!cascadeLoading && !cascadePreviewFailed && cascadeDetail?.notEnumerated?.map(item => <p key={item} className="text-xs text-theme-text-secondary">未枚举：{item}。</p>)}
        <CrossplaneDeleteNotice resources={[resourceData]} force={forceDelete} />

        <label className="flex items-center gap-2 text-sm text-theme-text-secondary">
          <input
            type="checkbox"
            checked={forceDelete}
            onChange={(e) => setForceDelete(e.target.checked)}
            className="w-4 h-4 rounded border-theme-border bg-theme-base text-red-600 focus:ring-red-500 focus:ring-offset-0"
          />
          <span>强制删除（移除 finalizer 并绕过宽限期）</span>
        </label>
      </div>
    </ConfirmDialog>
  )
}

const MAX_NAMES_PER_KIND = 8

// How a managed resource opts out of its controller's teardown. A Helm
// uninstall keeps resources by Helm's own annotation, not Flux's.
function optOut(teardown: NonNullable<CascadeDetail['controllerTeardown']>): string | undefined {
  if (teardown.action === 'uninstall') return 'helm.sh/resource-policy: keep'
  if (teardown.controller === 'Argo CD') return 'argocd.argoproj.io/sync-options: Delete=false'
  if (teardown.controller === 'Flux') return 'kustomize.toolkit.fluxcd.io/prune: disabled'
  return undefined
}

function ControllerTeardownNotice({ teardown, force }: { teardown: NonNullable<CascadeDetail['controllerTeardown']>; force: boolean }) {
  const resources = teardown.resources ?? []
  const destinationNotice = teardown.destination === 'remote'
    ? '此控制器的目标位置无法确认是当前集群。Radar 无法列出它将在该位置删除的资源。'
    : teardown.destination === 'unknown'
      ? '目标集群未知。Radar 无法显示控制器将在何处执行清理。'
      : ''
  const what = teardown.action === 'uninstall' ? '卸载 Helm release' : '删除它管理的资源'
  if (force) {
    return (
      <AlertBanner
        variant="warning"
        title="可能跳过控制器清理"
        message={<>
          {destinationNotice && <>{destinationNotice} </>}
          {teardown.terminating
            ? `该资源已开始删除，因此 ${teardown.controller} 可能已开始${what}。强制删除会移除其 finalizer，但无法撤销已发生的操作。`
            : `强制删除会移除 ${teardown.controller} 的 finalizer，因此可能跳过其剩余清理。清理可能已经开始；移除 finalizer 无法撤销已发生的操作。`}
        </>}
        className="mb-0"
      />
    )
  }
  const title = teardown.destination === 'unknown'
    ? `${teardown.controller} 有清理 finalizer`
    : teardown.action === 'uninstall'
    ? `${teardown.controller} 配置为卸载 Helm release`
    : `${teardown.controller} 配置为清理受管资源`
  return (
    <CascadeDependentsList
      dependents={resources}
      title={resources.length > 0 ? `${title}（显示 ${resources.length} 个）` : title}
      summary={`${destinationNotice ? destinationNotice + ' ' : ''}${teardown.action === 'uninstall' ? 'Helm 会卸载其存储的 release 清单中记录的资源' : `${teardown.controller} 会删除其自身清单中记录的资源`}，其中可能包含 Radar 未显示的资源。${optOut(teardown) ? ` 标记为 ${optOut(teardown)} 的资源会保留。` : ''}`}
    />
  )
}

// `note` explains the expanded list; `summary` is a caveat that must read
// without expanding it.
function CascadeDependentsList({ dependents, title, note, summary }: { dependents: CascadeDependent[]; title: string; note?: string; summary?: string }) {
  const [expanded, setExpanded] = useState(false)
  const { panelId, buttonProps } = useDisclosure(expanded)

  const grouped = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const dep of dependents) {
      const kind = dep.group ? `${dep.kind} (${dep.group})` : dep.kind
      if (!map.has(kind)) map.set(kind, [])
      map.get(kind)!.push(dep.namespace ? `${dep.namespace}/${dep.name}` : dep.name)
    }
    return Array.from(map.entries()).sort((a, b) => a[0].localeCompare(b[0]))
  }, [dependents])

  return (
    <div className="rounded border border-amber-500/30 bg-amber-500/5">
      <button
        {...buttonProps}
        type="button"
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-2 w-full px-3 py-2 text-left text-xs font-medium text-amber-400 hover:bg-amber-500/10 transition-colors"
      >
        <CollapseChevron open={expanded} inheritColor className="w-3.5 h-3.5" />
        <span>{title}</span>
      </button>
      {summary && <p className="px-3 pb-2 text-xs text-theme-text-secondary">{summary}</p>}

      <Collapse open={expanded} id={panelId}>
        <div className="px-3 pb-2.5 space-y-1.5">
          {grouped.map(([kind, names]) => (
            <div key={kind} className="text-xs">
              <span className="font-medium text-theme-text-primary">{kind}</span>
              <span className="text-theme-text-tertiary ml-1">({names.length})</span>
              <div className="ml-3 mt-0.5 text-theme-text-secondary font-mono break-all">
                {names.slice(0, MAX_NAMES_PER_KIND).join(', ')}
                {names.length > MAX_NAMES_PER_KIND && (
                  <span className="text-theme-text-tertiary"> +{names.length - MAX_NAMES_PER_KIND} more</span>
                )}
              </div>
            </div>
          ))}
          {note && <p className="text-xs text-theme-text-tertiary">{note}</p>}
        </div>
      </Collapse>
    </div>
  )
}
