import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { PrivacySection } from './PrivacySection'

describe('PrivacySection', () => {
  it('shows a static truthful no-telemetry notice without an enable control', () => {
    const html = renderToString(<PrivacySection active />)
    expect(html).toContain('KubeAtlas 不收集或发送匿名使用情况统计')
    expect(html).toContain('旧的遥测授权和环境变量不会启用上报')
    expect(html).toContain('按各自配置访问')
    expect(html).not.toContain('aria-checked')
    expect(html).not.toContain('发送使用情况统计')
  })
})
