import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import {
  DrainPlanContent,
  canConfirmDrain,
  emptyDirPodsAtRisk,
  planMatches,
  type DrainPlan,
  type DrainPlanPod,
} from './DrainPlanDialog'

function pod(name: string, outcome: DrainPlanPod['outcome'], extra: Partial<DrainPlanPod> = {}): DrainPlanPod {
  return { namespace: 'shop', name, outcome, reason: `${name} reason`, emptyDir: false, pdbChecked: true, ...extra }
}

function plan(pods: DrainPlanPod[], options: Partial<DrainPlan['options']> = {}): DrainPlan {
  return {
    node: 'worker-1',
    generatedAt: '2026-09-10T10:00:00Z',
    estimate: true,
    options: { ignoreDaemonSets: true, deleteEmptyDirData: false, force: false, ...options },
    summary: {
      evict: pods.filter((p) => p.outcome === 'evict').length,
      skip: pods.filter((p) => p.outcome === 'skip').length,
      mayBlock: pods.filter((p) => p.outcome === 'may-block').length,
    },
    pods,
    pdbsEvaluated: true,
  }
}

const off = { force: false, deleteEmptyDirData: false }
const emptyDirOn = { force: false, deleteEmptyDirData: true }

describe('planMatches', () => {
  it('rejects a plan computed for another node or other options', () => {
    const p = plan([])
    expect(planMatches(p, 'worker-1', off)).toBe(true)
    expect(planMatches(p, 'worker-2', off)).toBe(false)
    expect(planMatches(p, 'worker-1', emptyDirOn)).toBe(false)
    expect(planMatches(null, 'worker-1', off)).toBe(false)
  })
})

describe('canConfirmDrain', () => {
  it('never enables the drain before a current plan exists', () => {
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: off, loading: false, planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: plan([]), nodeName: 'worker-1', options: off, loading: true, planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: plan([]), nodeName: 'other', options: off, loading: false, planSupported: true })).toBe(false)
  })

  it('keeps the drain disabled after a plan request error, even if an older plan is still around', () => {
    const p = plan([pod('web', 'evict')])
    expect(canConfirmDrain({ plan: p, nodeName: 'worker-1', options: off, loading: false, error: 'boom', planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: off, loading: false, error: 'boom', planSupported: false })).toBe(true)
  })

  it('enables the drain once a matching plan is shown', () => {
    expect(canConfirmDrain({ plan: plan([pod('web', 'evict')]), nodeName: 'worker-1', options: off, loading: false, planSupported: true })).toBe(true)
  })

  it('does not gate the drain a second time when emptyDir pods would be evicted', () => {
    const p = plan([pod('cache', 'evict', { emptyDir: true })], { deleteEmptyDirData: true })
    expect(emptyDirPodsAtRisk(p).map((x) => x.name)).toEqual(['cache'])
    expect(canConfirmDrain({ plan: p, nodeName: 'worker-1', options: emptyDirOn, loading: false, planSupported: true })).toBe(true)
  })

  it('confirms without a plan when the host cannot compute one', () => {
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: off, loading: false, planSupported: false })).toBe(true)
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: emptyDirOn, loading: false, planSupported: false })).toBe(true)
  })
})

describe('DrainPlanContent', () => {
  const noop = () => {}

  function render(props: Partial<Parameters<typeof DrainPlanContent>[0]>) {
    return renderToString(
      <DrainPlanContent
        nodeName="worker-1"
        plan={null}
        loading={false}
        options={off}
        onOptionsChange={noop}
        planSupported
        {...props}
      />,
    )
  }

  it('says a drain plan needs a newer Radar when the cluster predates it', () => {
    const html = render({
      planSupported: false,
      planUpgrade: { feature: 'Drain plans', minimumVersion: 'v1.14.0', currentVersion: 'v1.13.1' },
    })
    expect(html).toContain('驱逐计划')
    expect(html).toContain('需要 Radar v1.14 或更高版本。')
    expect(html).not.toContain('正在计算计划')
  })

  it('shows per-pod outcomes with reasons and calls the result an estimate', () => {
    const html = render({ plan: plan([pod('web', 'may-block', { pdb: 'shop/web', reason: 'PodDisruptionBudget shop/web currently allows no disruptions' }), pod('agent', 'skip')]) })
    expect(html).toContain('可能受阻')
    expect(html).toContain('shop/web')
    expect(html).toContain('currently allows no disruptions')
    expect(html).toContain('agent reason')
    expect(html).toContain('估算时间')
    expect(html).toContain('关于此估算')
  })

  it('distinguishes expected exclusions from protected workloads before draining', () => {
    const html = render({ plan: plan([pod('agent', 'skip', { expectedSkip: true }), pod('bare', 'skip', { expectedSkip: false })]) })
    expect(html).toContain('按设计保留')
    expect(html).toContain('需要关注')
    expect(html).toContain('bare reason')
  })

  it('offers a refresh only when the host can recompute the plan', () => {
    const p = plan([pod('web', 'evict')])
    expect(render({ plan: p, onRefreshPlan: noop })).toContain('重新计算计划')
    expect(render({ plan: p })).not.toContain('重新计算计划')
  })

  it('exposes the estimate caveat on a focusable trigger', () => {
    const html = render({ plan: plan([pod('web', 'evict')]) })
    expect(html).toMatch(/<button[^>]*aria-label="关于此估算"/)
  })

  it('offers a retry on a failed plan only when the host can recompute it', () => {
    expect(render({ error: 'boom', onRefreshPlan: noop })).toContain('请重试')
    expect(render({ error: 'boom' })).not.toContain('请重试')
  })

  it('shows a loading state instead of a stale plan', () => {
    const html = render({ loading: true, plan: plan([pod('web', 'evict')]) })
    expect(html).toContain('正在计算计划')
    expect(html).not.toContain('web reason')
  })

  it('ignores a plan computed for another node', () => {
    const html = render({ plan: { ...plan([pod('web', 'evict')]), node: 'worker-2' } })
    expect(html).not.toContain('web reason')
  })

  it('names the pods whose emptyDir data would go, and asks for no second tick', () => {
    const p = plan([pod('cache', 'evict', { emptyDir: true }), pod('cache-2', 'evict', { emptyDir: true }), pod('web', 'evict')], { deleteEmptyDirData: true })
    const html = render({ plan: p, options: emptyDirOn })
    expect(html).toContain('emptyDir 数据将被丢弃')
    expect(html).toContain('shop/cache')
    expect(html).toContain('将驱逐的 2 个 Pod 使用 emptyDir：shop/cache, shop/cache-2。')
    expect(html).not.toContain('2 个 Pods')
    expect(html).not.toMatch(/<input[^>]*type="checkbox"[^>]*class="mt-0\.5/)
  })

  it('says nothing about discarding emptyDir data while the option is off', () => {
    const html = render({ plan: plan([pod('cache', 'skip', { emptyDir: true })]) })
    expect(html).not.toContain('emptyDir 数据将被丢弃')
  })

  it('warns when PodDisruptionBudgets could not be evaluated and does not print a zero for them', () => {
    const p = { ...plan([pod('web', 'evict')]), pdbsEvaluated: false, pdbError: 'list poddisruptionbudgets: forbidden' }
    const html = render({ plan: p })
    expect(html).toContain('未评估 PodDisruptionBudget')
    expect(html).toContain('forbidden')
    expect(html).toContain('未评估 PodDisruptionBudget')
    expect(html).not.toContain('0 个可能受 PodDisruptionBudget 阻止')
  })

  it('warns honestly when the estimate shows no emptyDir pod', () => {
    const html = render({ plan: plan([pod('web', 'evict')], { deleteEmptyDirData: true }), options: emptyDirOn })
    expect(html).toContain('目前没有将被驱逐的 Pod 使用 emptyDir')
  })
})
