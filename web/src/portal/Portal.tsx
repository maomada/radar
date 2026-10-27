import { useEffect, useState } from 'react'
import { Activity, ArrowUpRight, BookOpen, Check, Copy, ExternalLink, Globe, Layers, Moon, Plug, Radar, RefreshCw, Search, Server, ShieldCheck, Sun } from 'lucide-react'
import { Badge } from '@skyhook-io/k8s-ui/components/ui/Badge'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'
import { filterClusters, mcpClientConfig, safeExternalURL } from './model'
import type { PortalCluster, PortalDirectory } from './model'

const connectionLabels = { connected: '已连接', connecting: '连接中', disconnected: '未连接' }
const authLabels = { none: '未启用认证', proxy: '代理认证', oidc: 'OIDC 登录' }

function timestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '未知' : date.toLocaleString('zh-CN', { hour12: false })
}

function CopyButton({ value, label }: { value: string; label: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'error'>('idle')
  useEffect(() => {
    if (state === 'idle') return
    const timer = window.setTimeout(() => setState('idle'), 4000)
    return () => window.clearTimeout(timer)
  }, [state])
  return <div className="flex flex-col items-start gap-1">
    <button type="button" className="inline-flex items-center gap-1.5 rounded-md px-2 py-1.5 text-xs text-accent-text hover:bg-accent-muted" onClick={async () => {
      try { await navigator.clipboard.writeText(value); setState('copied') }
      catch { setState('error') }
    }} aria-label={label}>
      {state === 'copied' ? <Check size={14} /> : <Copy size={14} />}
      <span aria-live="polite">{state === 'copied' ? '已复制' : label}</span>
    </button>
    {state === 'error' && <span role="status" className="text-xs text-theme-text-secondary">复制失败，请手动选择文本复制（剪贴板需要 HTTPS）。</span>}
  </div>
}

function Endpoint({ id, url, readOnly }: { id: string; url: string; readOnly: boolean }) {
  if (!safeExternalURL(url)) return <p className="text-sm text-theme-text-secondary">MCP 地址无效，请检查集群注册配置。</p>
  return <div className="rounded-lg border border-theme-border bg-theme-base p-3">
    <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
      <span className="text-xs font-medium text-theme-text-secondary">{readOnly ? '只读端点 · 推荐用于诊断' : '完整端点 · 包含写操作'}</span>
      <Badge tone={readOnly ? 'accent1' : 'note'} size="sm">{readOnly ? 'READ ONLY' : 'READ / WRITE'}</Badge>
    </div>
    <code className="portal-mono block break-all text-xs leading-relaxed text-theme-text-primary">{url}</code>
    <div className="mt-2 flex flex-wrap gap-2">
      <CopyButton value={url} label="复制地址" />
      <CopyButton value={mcpClientConfig(id, url)} label="复制 MCP 配置" />
    </div>
  </div>
}

function ClusterCard({ cluster, outdated }: { cluster: PortalCluster; outdated: boolean }) {
  const radarURL = safeExternalURL(cluster.radarUrl)
  const stale = cluster.status === 'stale'
  return <article className="portal-card flex min-w-0 flex-col overflow-hidden" aria-label={cluster.name}>
    <div className="flex-1 p-5">
      <div className="mb-4 flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <div className="rounded-xl bg-accent-muted p-2.5 text-accent-text"><Server size={23} /></div>
          <div className="min-w-0">
            <h2 className="break-words text-lg font-semibold">{cluster.name}</h2>
            <p className="portal-mono mt-0.5 break-all text-xs text-theme-text-tertiary">{cluster.id}</p>
          </div>
        </div>
        <Badge severity={outdated ? 'neutral' : stale ? 'warning' : 'info'}>{outdated ? '状态待更新' : stale ? '心跳过期' : '心跳在线'}</Badge>
      </div>
      <p className="min-h-10 break-words text-sm leading-relaxed text-theme-text-secondary">{cluster.description || '未提供集群描述'}</p>
      <div className="mt-3 flex flex-wrap gap-2">
        <Badge tone="structural">{cluster.runtimeMode === 'local' ? '本地 Radar' : cluster.runtimeMode === 'in-cluster' ? '集群内 Radar' : '运行模式未上报'}</Badge>
        {cluster.environment && <Badge tone="accent2">{cluster.environment}</Badge>}
        {cluster.region && <Badge tone="structural"><Globe size={12} className="mr-1" />{cluster.region}</Badge>}
      </div>
      <dl className="mt-5 grid grid-cols-2 gap-4 border-t border-theme-border pt-4 text-xs">
        <div><dt className="text-theme-text-tertiary">Radar 版本</dt><dd className="portal-mono mt-1 break-all">{cluster.radarVersion || '未上报'}</dd></div>
        <div><dt className="text-theme-text-tertiary">Kubernetes 连接 · 上次上报</dt><dd className="mt-1">{connectionLabels[cluster.connectionState] || '未知'}</dd></div>
        {cluster.runtimeMode === 'local' && <div className="col-span-2"><dt className="text-theme-text-tertiary">Context · 上次上报，不锁定跳转目标</dt><dd className="portal-mono mt-1 break-all">{cluster.kubeContext || '尚未确认'}</dd></div>}
        {cluster.clusterName && <div className="col-span-2"><dt className="text-theme-text-tertiary">Kubernetes 集群 · 上次上报</dt><dd className="portal-mono mt-1 break-all">{cluster.clusterName}</dd></div>}
        <div className="col-span-2"><dt className="text-theme-text-tertiary">Radar 访问地址</dt><dd className="portal-mono mt-1 break-all">{cluster.radarUrl}</dd></div>
      </dl>
      <div className="mt-5 border-t border-theme-border pt-4">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <h3 className="flex items-center gap-2 text-sm font-medium"><Plug size={16} className="text-accent-text" />Radar MCP Server</h3>
          <span className="text-xs text-theme-text-tertiary">{cluster.mcp.mode === 'disabled' ? '未启用' : 'Streamable HTTP'}</span>
        </div>
        <div className="space-y-2">
          {cluster.mcp.mode !== 'disabled' && cluster.mcp.readOnlyUrl && <Endpoint id={cluster.id} url={cluster.mcp.readOnlyUrl} readOnly />}
          {cluster.mcp.mode === 'full' && cluster.mcp.url && <Endpoint id={cluster.id} url={cluster.mcp.url} readOnly={false} />}
          {cluster.mcp.mode === 'disabled' && <p className="rounded-lg bg-theme-base p-3 text-xs leading-relaxed text-theme-text-tertiary">此 Radar 已关闭 MCP。启用后，接口信息将随下次心跳自动更新。</p>}
        </div>
        <p className="mt-3 flex items-start gap-1.5 text-xs leading-relaxed text-theme-text-tertiary"><ShieldCheck size={14} className="mt-0.5 shrink-0" />{authLabels[cluster.authMode] || '认证方式未知'} · 连接目标 Radar 时需使用其自身的访问权限。</p>
      </div>
    </div>
    <footer className="flex flex-wrap items-center justify-between gap-3 border-t border-theme-border bg-theme-base px-5 py-3">
      <div className="text-xs leading-relaxed text-theme-text-tertiary"><span className="block">最近心跳</span><time dateTime={cluster.lastSeen}>{timestamp(cluster.lastSeen)}</time></div>
      {radarURL ? <a href={radarURL} target="_blank" rel="noopener noreferrer" className="btn-brand inline-flex items-center gap-2 px-3 py-2 text-sm">打开 Radar <ArrowUpRight size={16} /></a> : <span className="text-xs text-theme-text-secondary">Radar 地址无效</span>}
    </footer>
  </article>
}

function Onboarding() {
  return <section id="onboarding" className="portal-card mt-10 scroll-mt-6 p-6" aria-labelledby="onboarding-title">
    <div className="flex flex-wrap items-center gap-2"><BookOpen size={19} className="text-accent-text" /><h2 id="onboarding-title" className="font-semibold">接入一个新的 Radar</h2><Badge tone="note">本地 / 集群内模式</Badge></div>
    <div className="mt-5 grid gap-6 lg:grid-cols-3">
      <div><p className="mb-2 text-sm font-medium"><span className="mr-2 text-accent-text">01</span>在门户授权实例标识</p><p className="text-sm leading-relaxed text-theme-text-secondary">为每个 Radar 实例分配唯一 ID 和独立注册令牌，通过门户 Secret 中的 tokens 映射授权，然后重启门户。令牌只允许该实例更新自己的记录。</p></div>
      <div><p className="mb-2 text-sm font-medium"><span className="mr-2 text-accent-text">02</span>配置 Radar 注册</p><p className="text-sm leading-relaxed text-theme-text-secondary">集群内通过 Helm 的 portalRegistration 和 existingSecret 配置。本地通过 RADAR_PORTAL_* 环境变量配置门户地址、实例 ID、名称、可访问地址和令牌。两种模式默认均不注册。</p></div>
      <div><p className="mb-2 text-sm font-medium"><span className="mr-2 text-accent-text">03</span>等待自动注册与心跳</p><p className="text-sm leading-relaxed text-theme-text-secondary">Radar 启动后主动注册，每 30 秒更新。门户无需连接 Kubernetes；浏览器和 MCP 客户端必须能直接访问目标 Radar。删除授权并重启门户可取消纳管。</p></div>
    </div>
    <p className="mt-5 text-sm leading-relaxed text-theme-text-secondary">本地 Radar 以实例纳管，不会枚举 kubeconfig 中的所有集群；切换 context 后，下次心跳更新该实例的上下文信息。跳转及 MCP 操作针对 Radar 当时选中的集群，请先确认目标。localhost 只指向访问者自己的电脑，跨机器访问需配置可达地址、网络和 Radar 认证；门户不会自动开放本地监听。</p>
    <div className="mt-5 flex items-start gap-2 border-t border-theme-border pt-4 text-xs leading-relaxed text-theme-text-tertiary"><ShieldCheck size={16} className="shrink-0" /><p>注册令牌不是 MCP 凭据。门户不转发登录信息、不代理 Kubernetes 操作。MCP 配置使用支持 HTTP 远程连接的 mcpServers 格式；请按客户端要求另行配置目标认证。生产环境请为门户及 Radar 启用 HTTPS。</p></div>
  </section>
}

export function Portal() {
  const [directory, setDirectory] = useState<PortalDirectory>()
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [refresh, setRefresh] = useState(0)
  const [query, setQuery] = useState('')
  const [environment, setEnvironment] = useState('')
  const [status, setStatus] = useState('')
  const [dark, setDark] = useState(() => {
    try { return localStorage.getItem('radar-portal-theme') === 'dark' }
    catch { return false }
  })
  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
    try { localStorage.setItem('radar-portal-theme', dark ? 'dark' : 'light') } catch { /* Storage may be disabled. */ }
  }, [dark])
  useEffect(() => {
    let disposed = false
    let timer: number | undefined
    const controller = new AbortController()
    async function load() {
      setLoading(true)
      const timeout = window.setTimeout(() => controller.abort(), 10000)
      try {
        const response = await fetch(apiUrl('/clusters'), { headers: getAuthHeaders(), credentials: getCredentialsMode(), signal: controller.signal, cache: 'no-store' })
        if (!response.ok) throw new Error(response.status === 401 ? '门户登录已失效，请重新加载页面并登录。' : `读取目录失败（HTTP ${response.status}）`)
        const data: PortalDirectory = await response.json()
        if (!Array.isArray(data.clusters) || !data.serverTime || !data.staleAfterSeconds) throw new Error('门户返回了无效的集群目录。')
        if (!disposed) { setDirectory(data); setError('') }
      } catch (cause) {
        if (!disposed) setError(cause instanceof Error && cause.name !== 'AbortError' ? cause.message : '连接门户超时，请检查网络并重试。')
      } finally {
        window.clearTimeout(timeout)
        if (!disposed) { setLoading(false); timer = window.setTimeout(() => setRefresh(value => value + 1), 15000) }
      }
    }
    void load()
    return () => { disposed = true; controller.abort(); window.clearTimeout(timer) }
  }, [refresh])

  const clusters = directory?.clusters ?? []
  const environments = [...new Set(clusters.map(cluster => cluster.environment).filter(Boolean))].sort()
  const filtered = filterClusters(clusters, query, environment, status)
  const online = clusters.filter(cluster => cluster.status === 'online').length
  const mcp = clusters.filter(cluster => cluster.mcp.mode !== 'disabled').length
  const unknown = !directory
  const summaries = [
    { label: '已纳管集群', value: clusters.length, icon: Layers, detail: '持久化集群目录' },
    { label: '心跳在线', value: online, icon: Activity, detail: '注册通道正常，非健康检查' },
    { label: '心跳过期', value: clusters.length - online, icon: RefreshCw, detail: `超过 ${directory?.staleAfterSeconds ?? 90} 秒未收到上报` },
    { label: 'MCP 已启用', value: mcp, icon: Plug, detail: '由各 Radar 上报的接口配置' },
  ]

  return <div className="min-h-screen">
    <header className="border-b border-theme-border bg-theme-surface">
      <div className="mx-auto flex max-w-[1800px] flex-wrap items-center justify-between gap-4 px-5 py-4 lg:px-10">
        <a href="/" className="flex items-center gap-3" aria-label="Radar 集群门户首页"><Radar size={29} className="text-accent-text" /><span className="text-xl font-semibold">Radar</span><span className="border-l border-theme-border pl-3 text-sm text-theme-text-secondary">集群门户</span></a>
        <div className="flex items-center gap-3"><span className="hidden items-center gap-1.5 text-xs text-theme-text-tertiary sm:flex"><ShieldCheck size={14} />独立部署 · 凭据隔离</span><button className="portal-control" onClick={() => setDark(value => !value)} aria-label={dark ? '切换浅色主题' : '切换深色主题'}>{dark ? <Sun size={17} /> : <Moon size={17} />}</button></div>
      </div>
    </header>
    <main className="mx-auto max-w-[1800px] px-5 py-8 lg:px-10">
      <div className="flex flex-wrap items-end justify-between gap-5">
        <div><p className="portal-mono mb-2 text-xs text-accent-text">FLEET DIRECTORY</p><h1 className="text-3xl font-semibold">集群总览</h1><p className="mt-3 max-w-2xl text-sm leading-relaxed text-theme-text-secondary">一个入口，连接所有 Radar。查看集群上报信息，进入独立控制台，或接入 MCP 工具。</p></div>
        <a href="#onboarding" className="portal-control inline-flex items-center gap-2"><BookOpen size={16} />集群接入指南 <ExternalLink size={14} /></a>
      </div>
      <section aria-label="目录摘要" className="my-7 grid grid-cols-2 gap-3 lg:grid-cols-4">
        {summaries.map(({ label, value, icon: Icon, detail }) => <div className="portal-card p-4 lg:p-5" key={label}><div className="flex items-center justify-between gap-2 text-sm text-theme-text-secondary"><span>{label}</span><Icon size={18} className="text-accent-text" /></div><p className="my-3 text-3xl font-semibold">{unknown ? '—' : value}{error && directory && <span className="ml-2 text-xs font-normal text-theme-text-tertiary">上次记录</span>}</p><p className="text-xs leading-relaxed text-theme-text-tertiary">{detail}</p></div>)}
      </section>
      <section aria-label="集群目录">
        <div className="mb-4 flex flex-wrap items-center gap-3">
          <div className="relative min-w-48 flex-1"><Search size={17} className="pointer-events-none absolute left-3 top-3 text-theme-text-tertiary" /><input aria-label="搜索集群" className="portal-control w-full pl-10" placeholder="搜索名称、集群 ID、区域或地址…" value={query} onChange={event => setQuery(event.target.value)} /></div>
          <select aria-label="按环境筛选" className="portal-control" value={environment} onChange={event => setEnvironment(event.target.value)}><option value="">所有环境</option>{environments.map(value => <option key={value} value={value}>{value}</option>)}</select>
          <select aria-label="按心跳状态筛选" className="portal-control" value={status} onChange={event => setStatus(event.target.value)}><option value="">所有心跳状态</option><option value="online">心跳在线</option><option value="stale">心跳过期</option></select>
          <button className="portal-control inline-flex items-center gap-2" onClick={() => setRefresh(value => value + 1)} disabled={loading}><RefreshCw size={16} className={loading ? 'animate-spin' : ''} />刷新</button>
        </div>
        <div className="mb-5 flex flex-wrap justify-between gap-2 text-xs text-theme-text-tertiary"><span>{directory ? `显示 ${filtered.length} / ${clusters.length} 个集群` : '等待门户数据'}</span><span>每 15 秒刷新{directory ? ` · 数据截至 ${timestamp(directory.serverTime)}` : ''}</span></div>
        {error && <div role="alert" className="portal-card mb-5 border-warning p-4 text-sm"><p className="font-medium">{error}</p><p className="mt-1 text-theme-text-secondary">{directory ? '以下为上次成功读取的记录，不能代表当前状态。' : '尚未读取到目录，无法确认纳管数量。'} 点击刷新重试。</p></div>}
        {!directory && loading && <div role="status" className="portal-card p-14 text-center text-sm text-theme-text-secondary">正在加载集群目录…</div>}
        {directory && filtered.length > 0 && <div className="grid items-start gap-5 xl:grid-cols-2 2xl:grid-cols-3">{filtered.map(cluster => <ClusterCard key={cluster.id} cluster={cluster} outdated={Boolean(error)} />)}</div>}
        {directory && filtered.length === 0 && <div className="portal-card px-6 py-14 text-center"><Server size={32} className="mx-auto mb-4 text-accent-text" /><h2 className="font-semibold">{clusters.length ? '没有匹配的集群' : '等待第一个 Radar 接入'}</h2><p className="mx-auto mt-2 max-w-lg text-sm leading-relaxed text-theme-text-secondary">{clusters.length ? '尝试其他关键词，或清除环境和心跳筛选。' : '完成集群授权和 Radar 注册配置后，集群会自动出现在这里。无需向门户提供 kubeconfig。'}</p>{clusters.length ? <button className="portal-control mt-5" onClick={() => { setQuery(''); setEnvironment(''); setStatus('') }}>清除筛选</button> : <a href="#onboarding" className="mt-5 inline-block text-sm text-accent-text">查看接入步骤 →</a>}</div>}
      </section>
      <p className="mt-5 flex items-start gap-2 text-xs leading-relaxed text-theme-text-tertiary"><Activity size={15} className="mt-0.5 shrink-0" />心跳仅表示 Radar 最近向门户报到，不代表集群健康，也不保证 Radar 页面或 MCP 接口可从你的网络访问。</p>
      <Onboarding />
      <footer className="mt-8 flex flex-wrap justify-between gap-3 text-xs text-theme-text-tertiary"><span>Radar Portal · 集中展示，独立访问</span><span>本地与集群内 Radar 均可选择注册</span></footer>
    </main>
  </div>
}
