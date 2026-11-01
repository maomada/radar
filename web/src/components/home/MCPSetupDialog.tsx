import { useRef, useEffect, useState, useCallback } from 'react'
import { X, Copy, Check, Radio, Terminal, MessageSquare, Code2, Pin } from 'lucide-react'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { MCP_TOOL_CATALOG } from './mcpToolCatalog'
import { Tooltip } from '../ui/Tooltip'
import { Disclosure } from '@skyhook-io/k8s-ui/components/ui/Disclosure'

interface MCPSetupDialogProps {
  open: boolean
  onClose: () => void
  readOnly?: boolean
  execTools?: boolean
  mcpUrl: string
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)

  const handleCopy = () => {
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <Tooltip content="复制到剪贴板" position="left" wrapperClassName="absolute top-2 right-2">
    <button
      onClick={handleCopy}
      className="p-1.5 rounded-md bg-theme-elevated/50 hover:bg-theme-elevated text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
    >
      {copied ? <Check className="w-3.5 h-3.5 text-green-500" /> : <Copy className="w-3.5 h-3.5" />}
    </button>
    </Tooltip>
  )
}

function CodeBlock({ children }: { children: string }) {
  return (
    <div className="relative group">
      <pre className="bg-theme-base rounded-md px-3 py-2.5 text-xs font-mono text-theme-text-secondary overflow-x-auto whitespace-pre-wrap break-all">
        {children}
      </pre>
      <CopyButton text={children} />
    </div>
  )
}

export function MCPSetupDialog({ open, onClose, mcpUrl, readOnly = false, execTools }: MCPSetupDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const [isDesktop, setIsDesktop] = useState(false)
  const [portPinned, setPortPinned] = useState(false)
  const [pinning, setPinning] = useState(false)
  const [pinSuccess, setPinSuccess] = useState(false)
  const [pinError, setPinError] = useState('')

  useEffect(() => {
    if (!open) return
    setPinSuccess(false)
    setPinError('')
    fetch(apiUrl('/config'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then((res) => res.ok ? res.json() : null)
      .then((data) => {
        if (data) {
          setIsDesktop(data.isDesktop ?? false)
          setPortPinned(data.file?.port != null && data.file.port > 0)
        }
      })
      .catch((err) => console.warn('[mcp-setup] Failed to load config:', err))
  }, [open])

  const handlePinPort = useCallback(async () => {
    const currentPort = Number(window.location.port) || 80
    setPinning(true)
    setPinError('')
    try {
      const configRes = await fetch(apiUrl('/config'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      if (!configRes.ok) {
        setPinError('Failed to load current config')
        return
      }
      const configData = await configRes.json()
      const updated = { ...configData.file, port: currentPort }
      const res = await fetch(apiUrl('/config'), {
        method: 'PUT',
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify(updated),
      })
      if (res.ok) {
        setPortPinned(true)
        setPinSuccess(true)
      } else {
        setPinError('Failed to save port configuration')
      }
    } catch {
      setPinError('Failed to pin port — check server connection')
    } finally {
      setPinning(false)
    }
  }, [])

  useEffect(() => {
    if (!open) return
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [open, onClose])

  useEffect(() => {
    if (open && dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [open])

  if (!open) return null

  const currentPort = Number(window.location.port) || 80

  const tools = MCP_TOOL_CATALOG
    .filter((tool) => (!readOnly || !tool.write) && (tool.name !== 'exec_pod' || execTools))
    .map((tool) => readOnly && tool.name === 'diagnose'
      ? { ...tool, params: tool.params.filter((param) => param.arg !== 'in_cluster') }
      : tool)

  const mcpServersUrlConfig = (key: 'url' | 'serverUrl') => JSON.stringify({
    mcpServers: {
      radar: {
        [key]: mcpUrl,
      }
    }
  }, null, 2)

  const vsCodeConfig = JSON.stringify({
    servers: {
      radar: {
        type: "http",
        url: mcpUrl,
      }
    }
  }, null, 2)

  const opencodeConfig = JSON.stringify({
    mcp: {
      radar: {
        type: "remote",
        url: mcpUrl,
        enabled: true,
      }
    }
  }, null, 2)

  const clineConfig = JSON.stringify({
    mcpServers: {
      radar: {
        type: "streamableHttp",
        url: mcpUrl,
      }
    }
  }, null, 2)

  // mcp-remote rejects plain http:// unless the host is localhost/127.0.0.1 or --allow-http is passed.
  const parsedMcpUrl = new URL(mcpUrl)
  const mcpRemoteNeedsAllowHttp = parsedMcpUrl.protocol === 'http:' && parsedMcpUrl.hostname !== 'localhost' && parsedMcpUrl.hostname !== '127.0.0.1'
  const mcpRemoteArgs = ['-y', 'mcp-remote', mcpUrl, ...(mcpRemoteNeedsAllowHttp ? ['--allow-http'] : [])]
  const claudeDesktopConfig = JSON.stringify({
    mcpServers: {
      radar: {
        command: "npx",
        args: mcpRemoteArgs,
      }
    }
  }, null, 2)

  const zedConfig = JSON.stringify({
    context_servers: {
      radar: {
        url: mcpUrl,
      }
    }
  }, null, 2)

  const geminiConfig = JSON.stringify({
    mcpServers: {
      radar: {
        httpUrl: mcpUrl,
      }
    }
  }, null, 2)

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="relative dialog max-w-3xl w-full mx-4 outline-none max-h-[85vh] flex flex-col"
      >
        {/* Header */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-theme-border/50 shrink-0">
          <div className="flex items-center gap-3">
            <Radio className="w-5 h-5 text-purple-400" />
            <h3 className="text-lg font-semibold text-theme-text-primary">MCP 服务</h3>
          </div>
          <button onClick={onClose} className="p-1.5 hover:bg-theme-elevated rounded-md transition-colors">
            <X className="w-5 h-5 text-theme-text-tertiary" />
          </button>
        </div>

        {/* Scrollable content */}
        <div className="overflow-y-auto flex-1 min-h-0 px-6 py-5 space-y-6">
          {/* Explanation */}
          <div className="space-y-3">
            <h4 className="text-sm font-semibold text-theme-text-primary">让 AI 连接你的集群</h4>
            <p className="text-sm text-theme-text-secondary leading-relaxed">
              Radar 提供{' '}
              <a href="https://modelcontextprotocol.io" target="_blank" rel="noopener noreferrer" className="text-purple-400 hover:text-purple-300 underline underline-offset-2">
                Model Context Protocol
              </a>{' '}
              （MCP）服务，让 AI 代理可以通过 Radar{readOnly ? '查看并诊断' : '查看、诊断并操作'}你的集群。
              与直接使用 kubectl 不同，Radar 为 AI 提供预处理后的增强数据——
              拓扑图、健康评估、去重事件和过滤后的日志——帮助它快速理解集群状态，
              无需在冗长的 YAML 输出上消耗上下文。
            </p>
            <p className="text-sm text-theme-text-secondary leading-relaxed">
              {readOnly ? '只读模式会阻止写入工具以及会创建临时 Kubernetes 资源的诊断选项，但不会限制 Radar UI 或 REST API。' : <>大多数读取工具不会改变集群状态。明确请求集群内路由探测时，最多会创建
              5 个自动删除的探测 Pod。写入工具（重启、调整副本数、同步、应用、节点驱逐）会标记为破坏性操作，
              让 AI 客户端在运行前提示并确认。</>}
            </p>
          </div>

          {/* Endpoint */}
          <div className="space-y-2">
            <h4 className="text-sm font-semibold text-theme-text-primary">端点</h4>
            <div className="relative">
              <div className="flex items-center gap-3 bg-theme-base rounded-md px-3 py-2.5">
                <span className="badge text-purple-400 bg-purple-500/10">HTTP</span>
                <code className="inline-code text-sm">{mcpUrl}</code>
              </div>
              <CopyButton text={mcpUrl} />
            </div>

            {isDesktop && !portPinned && (
              <div className="flex items-center gap-2 px-3 py-2 text-xs bg-amber-500/10 border border-amber-500/20 rounded-md">
                <span className="text-amber-700 dark:text-amber-300 flex-1">
                  每次重启端口都会变化。固定端口即可保持 MCP 端点稳定。
                </span>
                <button
                  onClick={handlePinPort}
                  disabled={pinning}
                  className="shrink-0 flex items-center gap-1 px-2.5 py-1 text-xs font-medium text-amber-800 dark:text-amber-200 hover:text-amber-900 dark:hover:text-white bg-amber-500/20 hover:bg-amber-500/30 rounded transition-colors disabled:opacity-50"
                >
                  <Pin className="w-3 h-3" />
                  固定端口 {currentPort}
                </button>
              </div>
            )}

            {isDesktop && pinSuccess && (
              <p className="text-xs text-green-700 dark:text-green-400/80 px-0.5">
                端口 {currentPort} 已固定。MCP 端点在重启后仍会保持稳定。
              </p>
            )}

            {isDesktop && pinError && (
              <p className="text-xs text-red-600 dark:text-red-400 px-0.5">
                {pinError}
              </p>
            )}

            {isDesktop && (
              <p className="text-xs text-theme-text-tertiary px-0.5">
                你可以在{' '}
                <button
                  onClick={() => { onClose(); window.dispatchEvent(new Event('radar:open-settings')) }}
                  className="text-purple-500 dark:text-purple-400 hover:underline underline-offset-2"
                >
                  设置
                </button>.
              </p>
            )}
          </div>

          {/* Setup instructions */}
          <div className="space-y-2">
            <h4 className="text-sm font-semibold text-theme-text-primary">连接你的 AI 工具</h4>

            {[
              { icon: Terminal, name: 'Claude Code', path: '', config: `claude mcp add --transport http radar ${mcpUrl}` },
              { icon: Terminal, name: 'Codex', path: '', config: `codex mcp add radar --url ${mcpUrl}` },
              { icon: Code2, name: 'Cursor', path: '~/.cursor/mcp.json', config: mcpServersUrlConfig('url') },
              { icon: Code2, name: 'VS Code (GitHub Copilot)', path: '.vscode/mcp.json', config: vsCodeConfig },
              { icon: Terminal, name: 'GitHub Copilot CLI', path: '', config: `copilot mcp add --transport http radar ${mcpUrl}` },
              { icon: Terminal, name: 'OpenCode', path: '~/.config/opencode/opencode.json', config: opencodeConfig },
              { icon: Code2, name: 'Google Antigravity', path: '~/.gemini/config/mcp_config.json', config: mcpServersUrlConfig('serverUrl') },
              { icon: Code2, name: 'Kiro', path: '~/.kiro/settings/mcp.json', config: mcpServersUrlConfig('url') },
              { icon: Code2, name: 'JetBrains AI Assistant / Junie', path: 'AI Assistant settings or ~/.junie/mcp/mcp.json', config: mcpServersUrlConfig('url') },
              { icon: Code2, name: 'Cline', path: 'Cline MCP settings (via UI)', config: clineConfig },
              { icon: Code2, name: 'Devin Desktop (formerly Windsurf)', path: '~/.config/devin/mcp_config.json', config: mcpServersUrlConfig('url') },
              { icon: MessageSquare, name: 'Claude Desktop', path: '~/Library/Application Support/Claude/claude_desktop_config.json', config: claudeDesktopConfig },
              { icon: Code2, name: 'Zed', path: 'Zed settings.json', config: zedConfig },
              { icon: Terminal, name: 'Gemini CLI (Code Assist Standard/Enterprise)', path: '~/.gemini/settings.json', config: geminiConfig },
            ].map((agent) => (
              <Disclosure
                key={agent.name}
                className="rounded-md border border-theme-border/50 bg-theme-base/30"
                summaryClassName="gap-2 px-3 py-2 select-none hover:bg-theme-hover/50 rounded-md transition-colors"
                summary={
                  <>
                    <agent.icon className="w-4 h-4 text-theme-text-tertiary" />
                    <span className="text-sm font-medium text-theme-text-primary">{agent.name}</span>
                    {agent.path && <span className="text-[10px] text-theme-text-tertiary ml-auto">{agent.path}</span>}
                  </>
                }
              >
                <div className="px-3 pb-3 pt-1">
                  <CodeBlock>{agent.config}</CodeBlock>
                </div>
              </Disclosure>
            ))}
          </div>

          {/* Available tools */}
          <div className="space-y-2">
            <div className="flex items-baseline justify-between">
              <h4 className="text-sm font-semibold text-theme-text-primary">工具</h4>
              <span className="text-[11px] text-theme-text-tertiary">{tools.length} 个工具</span>
            </div>
            {!readOnly && execTools === false && (
              <p className="text-xs text-theme-text-tertiary">
                <code className="inline-code">exec_pod</code> 已禁用。Radar 运维人员可通过使用 <code className="inline-code">--mcp-exec-tools</code>{' '}
                且不使用 <code className="inline-code">--disable-exec</code> 重启来启用容器命令。请参阅{' '}
                <a href="https://github.com/skyhook-io/radar/blob/main/docs/mcp.md#exec_pod"
                  target="_blank" rel="noopener noreferrer" className="text-accent hover:underline">
                  配置选项
                </a>{' '}以了解 Desktop 和 Helm 配置。Kubernetes 权限仍然适用。
              </p>
            )}
            <div className="grid grid-cols-1 gap-1.5">
              {tools.map((tool) => (
                <div key={tool.name} className="card-inner space-y-1.5">
                  <div className="flex items-center gap-2">
                    <code className="inline-code text-[11px]">{tool.name}</code>
                    {tool.write && (
                      <Tooltip content="Write tool — annotated as destructive">
                      <span className="badge-sm bg-amber-500/10 text-amber-600 dark:text-amber-400">
                        写入
                      </span>
                      </Tooltip>
                    )}
                  </div>
                  <p className="text-[11px] text-theme-text-tertiary leading-relaxed">{tool.desc}</p>
                  {tool.params.length > 0 && (
                    <div className="flex flex-wrap gap-1.5 pt-0.5">
                      {tool.params.map((p) => (
                        <Tooltip key={p.arg} content={p.desc}>
                        <span className="inline-code text-[11px]">
                          <span>{p.arg}</span>
                          {p.required && <span className="text-red-400">*</span>}
                        </span>
                        </Tooltip>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </div>
          </div>
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-6 py-3 border-t border-theme-border/50 shrink-0">
          <a
            href="https://github.com/skyhook-io/radar/blob/main/docs/mcp.md"
            target="_blank"
            rel="noopener noreferrer"
            className="text-xs text-theme-text-tertiary hover:text-purple-400 transition-colors"
          >
            文档
          </a>
          <button
            onClick={onClose}
            className="px-4 py-2 text-sm font-medium rounded-lg hover:bg-theme-elevated transition-colors text-theme-text-secondary"
          >
            关闭
          </button>
        </div>
      </div>
    </div>
  )
}
