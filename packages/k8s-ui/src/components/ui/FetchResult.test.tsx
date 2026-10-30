import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { FetchResult } from './FetchResult'

function shaped(message: string, status: number, data?: Record<string, unknown>) {
  return Object.assign(new Error(message), { status, data })
}

describe('FetchResult', () => {
  it('renders the loader when loading, ignoring error', () => {
    const html = renderToString(<FetchResult loading={true} error={shaped('forbidden', 403)} />)
    expect(html).toContain('加载中')
    expect(html).not.toContain('无权访问')
  })

  it('renders notFoundMessage when neither loading nor error (disabled-query fallback)', () => {
    const html = renderToString(<FetchResult loading={false} notFoundMessage="Pod not found" />)
    expect(html).toContain('Pod not found')
  })

  it('default notFoundMessage is "未找到资源"', () => {
    const html = renderToString(<FetchResult loading={false} />)
    expect(html).toContain('未找到资源')
  })

  it('shows "无权访问" headline and the server message on 403', () => {
    const html = renderToString(
      <FetchResult
        loading={false}
        error={shaped('no access to clusterroles (cluster-scoped resource requires explicit RBAC)', 403)}
      />,
    )
    expect(html).toContain('无权访问')
    expect(html).toContain('no access to clusterroles')
  })

  it('uses notFoundMessage and renders the server message on 404', () => {
    const html = renderToString(
      <FetchResult
        loading={false}
        error={shaped('pods web-1 not found', 404)}
        notFoundMessage="Pod not found"
      />,
    )
    expect(html).toContain('Pod not found')
    expect(html).toContain('pods web-1 not found')
  })

  it('renders "集群不可用" on 503', () => {
    const html = renderToString(
      <FetchResult loading={false} error={shaped('Resource cache not available', 503)} />,
    )
    expect(html).toContain('集群不可用')
    expect(html).toContain('Resource cache not available')
  })

  // kind_sync_failed is how Radar releases before kind_sync_timed_out spelled it.
  it.each(['kind_sync_timed_out', 'kind_sync_failed'])('does not blame the cluster for a %s 503', (code) => {
    const html = renderToString(
      <FetchResult
        loading={false}
        error={shaped('pods failed to load within the sync deadline', 503, { error_code: code })}
      />,
    )
    expect(html).not.toContain('集群不可用')
    expect(html).toContain('无法加载')
    expect(html).toContain('pods failed to load within the sync deadline')
  })

  it('blames Radar\'s collector, not the cluster, for a collector failure', () => {
    const html = renderToString(
      <FetchResult loading={false} error={shaped('Radar is not allowed to list pods', 503, { error_code: 'radar_forbidden' })} />,
    )
    expect(html).toContain('Radar 无法读取')
    expect(html).not.toContain('集群不可用')
  })

  it('keeps a disconnected cluster reading as unavailable', () => {
    const html = renderToString(
      <FetchResult loading={false} error={shaped('Not connected to cluster', 503, { error_code: 'cluster_disconnected' })} />,
    )
    expect(html).toContain('集群不可用')
    expect(html).not.toContain('Radar 无法读取')
  })

  it('renders a 重试 button only when onRetry is given', () => {
    const err = shaped('internal server error', 500)
    expect(renderToString(<FetchResult loading={false} error={err} onRetry={() => {}} />)).toContain('重试')
    expect(renderToString(<FetchResult loading={false} error={err} />)).not.toContain('重试')
  })

  it('renders "需要登录" on 401', () => {
    const html = renderToString(
      <FetchResult loading={false} error={shaped('Unauthorized', 401)} />,
    )
    expect(html).toContain('需要登录')
  })

  it("renders a generic 'Couldn't load' for other 5xx", () => {
    const html = renderToString(
      <FetchResult loading={false} error={shaped('internal server error', 500)} />,
    )
    expect(html).toContain('无法加载')
    expect(html).toContain('internal server error')
  })

  it('handles a network failure (Error without .status) via the generic branch', () => {
    const html = renderToString(
      <FetchResult loading={false} error={new Error('Failed to fetch')} />,
    )
    expect(html).toContain('无法加载')
    expect(html).toContain('Failed to fetch')
  })

  it('handles an AbortError-style throw without a status field', () => {
    const aborted = new Error('The user aborted a request.')
    aborted.name = 'AbortError'
    const html = renderToString(<FetchResult loading={false} error={aborted} />)
    expect(html).toContain('无法加载')
  })

  it('renders the Copy-error button when an error message is present', () => {
    const html = renderToString(
      <FetchResult loading={false} error={shaped('forbidden', 403)} />,
    )
    expect(html).toContain('复制错误')
  })
})
