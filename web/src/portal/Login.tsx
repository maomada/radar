import { useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowRight, Layers, LoaderCircle, LockKeyhole, Plug, Radar, ShieldCheck } from 'lucide-react'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'
import { ThemeToggle } from './ThemeToggle'

export function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const expired = new URLSearchParams(window.location.search).get('expired') === '1'

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const response = await fetch(apiUrl('/auth/login'), {
        method: 'POST', headers: { ...getAuthHeaders(), 'Content-Type': 'application/json' },
        credentials: getCredentialsMode(), body: JSON.stringify({ username, password }),
        signal: AbortSignal.timeout(10000),
      })
      if (!response.ok) {
        if (response.status === 401) throw new Error('账号或密码不正确，请重新输入。')
        if (response.status === 429) throw new Error('登录尝试过于频繁，请稍后再试。')
        throw new Error('暂时无法登录，请稍后重试。')
      }
      const session = await fetch(apiUrl('/auth/session'), {
        headers: getAuthHeaders(), credentials: getCredentialsMode(), cache: 'no-store', signal: AbortSignal.timeout(10000),
      })
      if (!session.ok) throw new Error('无法建立登录会话。请使用 HTTPS；本机 HTTP 调试需启用 --insecure-cookie。')
      window.location.replace('/')
    } catch (cause) {
      setError(cause instanceof Error && cause.name === 'Error' ? cause.message : '连接门户失败，请检查网络后重试。')
    } finally {
      setPassword('')
      setBusy(false)
    }
  }

  return <div className="flex min-h-screen flex-col">
    <header className="border-b border-theme-border bg-theme-surface">
      <div className="mx-auto flex max-w-7xl items-center justify-between px-6 py-5 lg:px-10">
        <a href="/login" aria-label="Radar 门户登录首页" className="flex items-center gap-3"><Radar size={29} className="text-accent-text" /><span className="text-xl font-semibold">Radar</span><span className="border-l border-theme-border pl-3 text-sm text-theme-text-secondary">集群门户</span></a>
        <ThemeToggle />
      </div>
    </header>
    <main className="mx-auto grid w-full max-w-6xl flex-1 content-center gap-10 px-6 py-12 lg:grid-cols-2 lg:items-center lg:gap-20 lg:px-10 lg:py-20">
      <section aria-label="门户介绍">
        <div className="mb-6 inline-flex rounded-2xl border border-theme-border bg-theme-surface p-4 text-accent-text"><Radar size={36} /></div>
        <p className="portal-mono mb-3 text-xs text-accent-text">RADAR PORTAL</p>
        <h1 className="text-3xl font-semibold leading-tight lg:text-4xl">一个入口，<br />连接所有 Radar。</h1>
        <p className="mt-5 max-w-md text-base leading-relaxed text-theme-text-secondary">登录集中式门户，查看纳管实例、进入集群控制台，获取 MCP Server 接入信息。</p>
        <div className="mt-8 hidden space-y-5 lg:block">
          {[
            { icon: Layers, title: '统一实例目录', detail: '本地与集群内 Radar，按环境与心跳状态检索。' },
            { icon: Plug, title: 'MCP 接入信息', detail: '集中查看接口地址，复制客户端连接配置。' },
            { icon: ShieldCheck, title: '独立认证边界', detail: '门户不代理集群操作，各 Radar 保留自己的认证。' },
          ].map(item => <div key={item.title} className="flex items-start gap-3"><item.icon size={20} className="mt-0.5 shrink-0 text-accent-text" /><div><p className="text-sm font-medium">{item.title}</p><p className="mt-1 text-sm text-theme-text-secondary">{item.detail}</p></div></div>)}
        </div>
      </section>
      <section aria-labelledby="login-title" className="portal-card p-6 shadow-theme-sm sm:p-9">
        <div className="mb-5 inline-flex rounded-xl bg-accent-muted p-3 text-accent-text"><LockKeyhole size={23} /></div>
        <h2 id="login-title" className="text-2xl font-semibold">登录集群门户</h2>
        <p className="mt-2 text-sm leading-relaxed text-theme-text-secondary">使用门户管理员配置的账号继续。</p>
        {expired && !error && <p role="status" className="mt-5 rounded-lg border border-theme-border bg-theme-base p-3 text-sm text-theme-text-secondary">登录会话已过期，请重新登录。</p>}
        <form className="mt-7 space-y-5" onSubmit={submit} aria-busy={busy}>
          <div><label htmlFor="portal-username" className="mb-2 block text-sm font-medium">账号</label><input id="portal-username" name="username" autoComplete="username" className="portal-control w-full" placeholder="请输入门户账号" value={username} onChange={event => setUsername(event.target.value)} required maxLength={256} disabled={busy} autoCapitalize="none" spellCheck={false} /></div>
          <div><label htmlFor="portal-password" className="mb-2 block text-sm font-medium">密码</label><input id="portal-password" name="password" type="password" autoComplete="current-password" className="portal-control w-full" placeholder="请输入密码" value={password} onChange={event => setPassword(event.target.value)} required maxLength={4096} disabled={busy} /></div>
          {error && <p role="alert" className="rounded-lg border border-theme-border bg-theme-base p-3 text-sm text-theme-text-secondary">{error}</p>}
          <button type="submit" className="btn-brand flex w-full items-center justify-center gap-2 py-3 text-sm font-medium" disabled={busy}>{busy ? <LoaderCircle size={17} className="animate-spin" /> : null}{busy ? '正在登录…' : '登录'}{!busy && <ArrowRight size={17} />}</button>
        </form>
        <p className="mt-6 border-t border-theme-border pt-5 text-xs leading-relaxed text-theme-text-tertiary">此账号仅用于访问门户目录，不是 Kubernetes 或目标 Radar 的登录凭据。忘记密码请联系门户管理员。</p>
      </section>
    </main>
    <footer className="px-6 pb-6 text-center text-xs text-theme-text-tertiary">Radar Portal · 独立部署，集中访问</footer>
  </div>
}
