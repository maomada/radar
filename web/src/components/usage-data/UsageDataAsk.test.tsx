import { renderToString } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { UsageDataStatus } from '../../api/usage-data'
import { exampleReport, exampleStatus } from '../../api/usage-data.fixtures'

vi.mock('../../api/usage-data', () => ({
  useSetUsageData: () => ({ mutate: vi.fn(), isPending: false }),
  useMarkUsagePromptShown: () => vi.fn(),
}))

import { USAGE_DATA_COPY, UsageDataAsk } from './UsageDataAsk'

function status(patch: Partial<UsageDataStatus>): UsageDataStatus {
  return exampleStatus({ preview: exampleReport({ views: {} }), ...patch })
}

const render = (s: UsageDataStatus | undefined) =>
  renderToString(<UsageDataAsk usageData={s} onReadMore={() => {}} />)

describe('UsageDataAsk', () => {
  it('asks an undecided user who can decide', () => {
    const html = render(status({}))
    expect(html).toContain(USAGE_DATA_COPY.title)
    expect(html).toContain(USAGE_DATA_COPY.description)
    expect(html).toContain(USAGE_DATA_COPY.readMore)
    expect(html).toContain(USAGE_DATA_COPY.send)
    expect(html).toContain(USAGE_DATA_COPY.decline)
  })

  it('keeps the usage prompt controls and acknowledgements in Chinese', () => {
    expect(USAGE_DATA_COPY).toMatchObject({
      title: '帮助改进Radar',
      description: '匿名统计，不使用第三方跟踪器。',
      readMore: '查看发送内容',
      send: '发送使用情况统计',
      decline: '暂不发送',
      close: '关闭',
      thanks: '谢谢。你可以随时在“设置”>“隐私”中更改。',
      declined: '不会发送任何内容。你可以随时在“设置”>“隐私”中更改。',
    })
  })

  it('stays out of the way once answered or where the viewer cannot decide', () => {
    expect(render(status({ state: 'on', source: 'user' }))).toBe('')
    expect(render(status({ state: 'off', source: 'user' }))).toBe('')
    expect(render(status({ state: 'off', source: 'deployment', canChange: false }))).toBe('')
    expect(render(status({ state: 'off', source: 'env', canChange: false }))).toBe('')
    expect(render(status({ shared: true, canChange: false, ask: false }))).toBe('')
    expect(render(undefined)).toBe('')
  })

  it('waits when the server says it asked recently', () => {
    expect(render(status({ ask: false }))).toBe('')
  })
})
