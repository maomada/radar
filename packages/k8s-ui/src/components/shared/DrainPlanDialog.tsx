import { Info, Loader2, RefreshCw } from 'lucide-react'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import { Badge, type BadgeSeverity } from '../ui/Badge'
import { AlertBanner } from '../ui/drawer-components'
import { Tooltip } from '../ui/Tooltip'
import { RadarUpgradeNote } from '../ui/RadarUpgradeNote'
import type { RadarUpgradeRequirement } from '../../types/fetch-error'

// Shapes returned by POST /api/nodes/{name}/drain-plan.
export type DrainOutcome = 'evict' | 'skip' | 'may-block'

export interface DrainPlanPod {
  namespace: string
  name: string
  outcome: DrainOutcome
  reason: string
  emptyDir: boolean
  pdb?: string
  pdbChecked: boolean
  terminating?: boolean
  expectedSkip?: boolean
}

export interface DrainPlan {
  node: string
  generatedAt: string
  estimate: boolean
  options: { ignoreDaemonSets: boolean; deleteEmptyDirData: boolean; force: boolean; gracePeriodSeconds?: number }
  summary: { evict: number; skip: number; mayBlock: number }
  pods: DrainPlanPod[]
  pdbsEvaluated: boolean
  pdbError?: string
}

// The two options the operator chooses in the dialog. Both are always sent
// explicitly to the drain endpoint; nothing relies on server-side defaults.
export interface DrainDialogOptions {
  force: boolean
  deleteEmptyDirData: boolean
}

export const DEFAULT_DRAIN_DIALOG_OPTIONS: DrainDialogOptions = { force: false, deleteEmptyDirData: false }

/** A plan is only usable when it was computed for this node with these options. */
export function planMatches(plan: DrainPlan | null | undefined, nodeName: string, options: DrainDialogOptions): plan is DrainPlan {
  return Boolean(
    plan &&
      plan.node === nodeName &&
      plan.options.force === options.force &&
      plan.options.deleteEmptyDirData === options.deleteEmptyDirData,
  )
}

/** Pods whose emptyDir data would be discarded by this drain (evicted, not skipped). */
export function emptyDirPodsAtRisk(plan: DrainPlan): DrainPlanPod[] {
  return plan.pods.filter((p) => p.emptyDir && p.outcome !== 'skip')
}

export interface CanConfirmDrainInput {
  plan: DrainPlan | null | undefined
  nodeName: string
  options: DrainDialogOptions
  loading: boolean
  /** the last plan request failed; with plan support the drain stays disabled until a plan is shown again */
  error?: string | null
  /** false when the host cannot compute plans (no onPlanDrain); the dialog then confirms without one */
  planSupported: boolean
}

/**
 * Whether the destructive Drain button may be enabled. With plan support the
 * operator must see a current plan first. Deleting emptyDir data is its own
 * deliberate choice, off by default and warned about by name, so it does not
 * gate the button a second time.
 */
export function canConfirmDrain({ plan, nodeName, options, loading, error, planSupported }: CanConfirmDrainInput): boolean {
  if (loading) return false
  if (planSupported && error) return false
  const current = planMatches(plan, nodeName, options) ? plan : null
  if (planSupported && !current) return false
  return true
}

const OUTCOME_SEVERITY: Record<DrainOutcome, BadgeSeverity> = {
  evict: 'warning',
  'may-block': 'error',
  skip: 'neutral',
}

const OUTCOME_LABEL: Record<DrainOutcome, string> = {
  evict: '驱逐',
  'may-block': '可能受阻',
  skip: '跳过',
}

const ESTIMATE_CAVEAT =
  '这是估算，并非保证：执行时 Drain 会重新读取实时状态，在此之前 Pod、预算和权限都可能发生变化。' +
  '通常 Drain 最多等待 2 分钟完成驱逐和删除 Pod；如果驱逐暂时受中断预算阻止，会重试。' +
  '较旧版本的 Radar 可能会在 Pod 删除前返回。若某个 Pod 同时受多个预算约束，系统会直接拒绝驱逐。'

interface DrainPlanContentProps {
  nodeName: string
  plan?: DrainPlan | null
  loading: boolean
  error?: string | null
  options: DrainDialogOptions
  onOptionsChange: (options: DrainDialogOptions) => void
  planSupported: boolean
  /** Why there is no plan, when the connected Radar predates drain plans. */
  planUpgrade?: RadarUpgradeRequirement | null
  /** Recompute the plan with the current options; also the retry after a failed plan. */
  onRefreshPlan?: () => void
}

/**
 * The body of the drain dialog: options, the plan table and the emptyDir
 * warning. Presentational, so it renders without a DOM (tests use
 * react-dom/server); the enclosing dialog owns the confirm gating.
 */
export function DrainPlanContent({
  nodeName, plan, loading, error, options, onOptionsChange, planSupported, planUpgrade, onRefreshPlan,
}: DrainPlanContentProps) {
  const current = planMatches(plan, nodeName, options) ? plan : null
  const atRisk = current ? emptyDirPodsAtRisk(current) : []

  return (
    <div className="flex flex-col gap-3 text-sm text-theme-text-secondary">
      <div className="flex flex-wrap gap-x-6 gap-y-2">
        <label className="flex items-center gap-2 cursor-pointer">
          <input
            type="checkbox"
            checked={options.force}
            onChange={(e) => onOptionsChange({ ...options, force: e.target.checked })}
            className="rounded border-theme-border"
          />
          强制驱逐：驱逐未由控制器管理的 Pod（这些 Pod 不会重新创建）
        </label>
        <label className="flex items-center gap-2 cursor-pointer">
          <input
            type="checkbox"
            checked={options.deleteEmptyDirData}
            onChange={(e) => onOptionsChange({ ...options, deleteEmptyDirData: e.target.checked })}
            className="rounded border-theme-border"
          />
          删除 emptyDir 数据：驱逐使用 emptyDir 卷的 Pod（其中的数据将丢失）
        </label>
      </div>

      {!planSupported && planUpgrade && <RadarUpgradeNote requirement={{ ...planUpgrade, feature: '驱逐计划' }} />}

      {planSupported && loading && (
        <div className="flex items-center gap-2 text-theme-text-secondary">
          <Loader2 className="w-4 h-4 animate-spin" /> 正在计算计划…
        </div>
      )}

      {planSupported && !loading && error && (
        <AlertBanner variant="error" title="无法计算驱逐计划" message={error}>
          {onRefreshPlan && (
            <button
              type="button"
              onClick={onRefreshPlan}
              className="mt-2 rounded border border-theme-border bg-theme-surface px-2 py-1 text-xs text-theme-text-primary transition-colors hover:bg-theme-hover"
            >
              请重试
            </button>
          )}
        </AlertBanner>
      )}

      {planSupported && !loading && !error && current && (
        <>
          <div className="flex items-start justify-between gap-2">
            <div className="text-theme-text-primary">
              计划驱逐 {current.summary.evict} 个 Pod，{' '}
              {current.pdbsEvaluated
                ? `${current.summary.mayBlock} 个可能受 PodDisruptionBudget 阻止`
                : '未评估 PodDisruptionBudget'}
              ，跳过 {current.summary.skip} 个。
            </div>
            <div className="flex items-center gap-1 shrink-0 text-xs text-theme-text-tertiary">
              <span>估算时间：{new Date(current.generatedAt).toLocaleTimeString()}</span>
              <Tooltip content={ESTIMATE_CAVEAT} className="max-w-xs">
                <button
                  type="button"
                  aria-label="关于此估算"
                  className="p-0.5 rounded text-theme-text-tertiary transition-colors hover:text-theme-text-secondary"
                >
                  <Info className="w-3.5 h-3.5" />
                </button>
              </Tooltip>
              {onRefreshPlan && (
                <Tooltip content="重新计算计划">
                  <button
                    type="button"
                    aria-label="重新计算计划"
                    onClick={onRefreshPlan}
                    className="p-1 rounded text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
                  >
                    <RefreshCw className="w-3.5 h-3.5" />
                  </button>
                </Tooltip>
              )}
            </div>
          </div>

          {!current.pdbsEvaluated && (
            <AlertBanner
              variant="warning"
              title="未评估 PodDisruptionBudget"
              message={current.pdbError ?? '无法列出预算；驱逐操作仍可能被拒绝。'}
            />
          )}

          <div className="max-h-72 overflow-auto rounded border border-theme-border">
            <table className="w-full text-xs">
              <tbody>
                {current.pods.map((p) => (
                  <tr key={`${p.namespace}/${p.name}`} className="border-b border-theme-border last:border-b-0 align-top">
                    <td className="px-2 py-1.5 whitespace-nowrap">
                      <Badge severity={p.outcome === 'skip' && p.expectedSkip === false ? 'warning' : OUTCOME_SEVERITY[p.outcome]} size="sm">
                        {p.outcome === 'skip' && p.expectedSkip !== undefined ? p.expectedSkip ? '按设计保留' : '需要关注' : OUTCOME_LABEL[p.outcome]}
                      </Badge>
                    </td>
                    <td className="px-2 py-1.5 font-mono whitespace-nowrap text-theme-text-primary">
                      {p.namespace}/{p.name}
                    </td>
                    <td className="px-2 py-1.5 text-theme-text-secondary">{p.reason}</td>
                  </tr>
                ))}
                {current.pods.length === 0 && (
                  <tr>
                    <td className="px-2 py-3 text-theme-text-secondary" colSpan={3}>此节点上没有已调度的 Pod。</td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </>
      )}

      {options.deleteEmptyDirData && (
        <AlertBanner
          variant="warning"
          title="emptyDir 数据将被丢弃"
          message={
            current && atRisk.length > 0
              ? `将驱逐的 ${atRisk.length} 个 Pod 使用 emptyDir：${atRisk.map((p) => `${p.namespace}/${p.name}`).join(', ')}。这些数据无法恢复。`
              : current
                ? '目前没有将被驱逐的 Pod 使用 emptyDir。驱逐执行时若有 Pod 使用此卷，其中数据将丢失且无法恢复。'
                : '所有使用 emptyDir 卷且被驱逐的 Pod，其数据都会丢失且无法恢复。'
          }
        />
      )}
    </div>
  )
}

interface DrainPlanDialogProps {
  open: boolean
  nodeName: string
  plan?: DrainPlan | null
  loading: boolean
  error?: string | null
  options: DrainDialogOptions
  onOptionsChange: (options: DrainDialogOptions) => void
  onConfirm: (options: DrainDialogOptions) => void
  onClose: () => void
  isDraining: boolean
  /** Whether the host can compute plans. Without it the dialog confirms without one. */
  planSupported: boolean
  /** Why there is no plan, when the connected Radar predates drain plans. */
  planUpgrade?: RadarUpgradeRequirement | null
  /** Recompute the plan with the current options; also the retry after a failed plan. */
  onRefreshPlan?: () => void
}

export function DrainPlanDialog({
  open, nodeName, plan, loading, error, options, onOptionsChange, onConfirm, onClose, isDraining, planSupported, planUpgrade, onRefreshPlan,
}: DrainPlanDialogProps) {
  const confirmEnabled = canConfirmDrain({ plan, nodeName, options, loading, error, planSupported })

  return (
    <ConfirmDialog
      open={open}
      onClose={onClose}
      onConfirm={() => onConfirm(options)}
      title="驱逐节点工作负载"
      message={planSupported
        ? `禁止节点 "${nodeName}" 调度并驱逐下方列出的 Pod。此列表仅为估算：执行驱逐时会重新读取实时状态。`
        : `禁止节点 "${nodeName}" 调度并驱逐其 Pod。`}
      confirmLabel={isDraining ? '正在驱逐…' : '驱逐'}
      variant="danger"
      isLoading={isDraining}
      isClosable
      confirmDisabled={!confirmEnabled}
      className="max-w-3xl"
    >
      <DrainPlanContent
        nodeName={nodeName}
        plan={plan}
        loading={loading}
        error={error}
        options={options}
        onOptionsChange={onOptionsChange}
        planSupported={planSupported}
        planUpgrade={planUpgrade}
        onRefreshPlan={onRefreshPlan}
      />
    </ConfirmDialog>
  )
}
