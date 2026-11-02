import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { PVCRenderer } from './PVCRenderer'

describe('PVCRenderer', () => {
  it('does not reassure indefinitely pending PVCs as normal', () => {
    const html = renderToString(
      <PVCRenderer
        data={{
          metadata: { name: 'data', namespace: 'demo' },
          spec: { storageClassName: 'standard' },
          status: { phase: 'Pending' },
        }}
      />,
    )

    expect(html).toContain('Pending')
    expect(html).toContain('尚未绑定')
    expect(html).toContain('检查 StorageClass')
    expect(html).not.toContain('This is normal')
    expect(html).not.toContain('expected indefinitely')
  })

  it('surfaces an unused claim as informational cleanup evidence', () => {
    const html = renderToString(
      <PVCRenderer
        data={{
          metadata: { name: 'archive', namespace: 'demo' },
          status: {
            phase: 'Bound',
            conditions: [
              {
                type: 'Unused',
                status: 'True',
                lastTransitionTime: '2026-08-28T12:00:00Z',
              },
            ],
          },
        }}
      />,
    )

    expect(html).toContain('Unused for about')
    expect(html).toContain('当前没有非终止状态的 Pod 引用此存储声明。')
    expect(html).toContain('删除前请确认保留策略和数据归属。')
    expect(html).toContain('bg-gray-400/20')
    expect(html).not.toContain('failing')
    expect(html).not.toContain('bg-emerald-500/20')
  })

  it('does not show cleanup guidance while Unused is false or absent', () => {
    for (const conditions of [
      [{ type: 'Unused', status: 'False' }],
      undefined,
    ]) {
      const html = renderToString(
        <PVCRenderer
          data={{
            metadata: { name: 'active', namespace: 'demo' },
            status: { phase: 'Bound', conditions },
          }}
        />,
      )
      expect(html).not.toContain('当前没有非终止状态的 Pod 引用此存储声明。')
    }
  })
})
