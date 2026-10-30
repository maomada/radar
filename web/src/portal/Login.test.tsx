import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Login } from './Login'

afterEach(() => vi.unstubAllGlobals())

describe('Portal login page', () => {
  it('renders a separate credential form without fetching or displaying the directory', () => {
    vi.stubGlobal('window', { location: { search: '' } })
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    const html = renderToStaticMarkup(<Login />)
    expect(html).toContain('登录集群门户')
    expect(html).toContain('autoComplete="username"')
    expect(html).toContain('autoComplete="current-password"')
    expect(html).toContain('type="password"')
    expect(html).not.toContain('搜索集群')
    expect(html).not.toContain('登录会话已过期')
    expect(fetch).not.toHaveBeenCalled()
  })

  it('explains an expired session on the login page', () => {
    vi.stubGlobal('window', { location: { search: '?expired=1' } })
    expect(renderToStaticMarkup(<Login />)).toContain('登录会话已过期，请重新登录。')
  })
})
