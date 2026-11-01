import { ServerOff, RefreshCw, Loader2, Copy, Check, TerminalSquare } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { ConnectionState } from '../context/ConnectionContext'
import { ContextSwitcher } from './ContextSwitcher'
import { parseContextName } from '../utils/context-name'
import { ClusterName } from '@skyhook-io/k8s-ui'
import { useOpenLocalTerminal } from './dock/DockContext'
import { useAuthMe, useContexts } from '../api/client'
import { Tooltip } from './ui/Tooltip'
import { Collapse, CollapseChevron } from '@skyhook-io/k8s-ui/components/ui/Collapse'
import { allShellSafe, awsProfileFlag } from '../utils/shell-safe'
import { apiUrl } from '../api/config'
import { useCapabilitiesContext } from '../contexts/CapabilitiesContext'

interface ConnectionErrorViewProps {
  connection: ConnectionState
  onRetry: () => void
  isRetrying: boolean
}

interface CommandHint {
  label: string
  command: string
  runnable?: boolean
}

interface AuthHints {
  title: string
  hints: string[]
  /** Primary auth command — usually sufficient on its own */
  authCommand?: CommandHint
  /** Secondary command shown as fallback if primary doesn't resolve the issue */
  fallbackCommand?: CommandHint
  /** Set when authCommand is a diagnostic rather than a re-auth — suppresses the "Authenticate in terminal" button */
  hideAuthButton?: boolean
}

function getAuthHints(context: string, awsProfile?: string): AuthHints {
  const parsed = parseContextName(context)

  switch (parsed.provider) {
    case 'GKE': {
      const result: AuthHints = {
        title: 'GKE 身份验证失败',
        hints: ['Radar 无法获取此上下文的 Google Cloud 凭据。'],
        authCommand: { label: '刷新 Google Cloud 凭据：', command: 'gcloud auth login' },
      }
      if (parsed.region && parsed.account && allShellSafe(parsed.clusterName, parsed.region, parsed.account)) {
        const isZone = /^[a-z]+-[a-z]+\d+-[a-z]$/.test(parsed.region)
        const flag = isZone ? '--zone' : '--region'
        result.fallbackCommand = {
          label: '如果仍无效，请刷新集群凭据：',
          command: `gcloud container clusters get-credentials ${parsed.clusterName} ${flag} ${parsed.region} --project ${parsed.account}`,
        }
      }
      return result
    }
    case 'EKS': {
      const profileFlag = awsProfileFlag(awsProfile)
      const result: AuthHints = {
        title: 'EKS 身份验证失败',
        hints: [
          'Radar 无法获取此上下文的 AWS 凭据。',
          '对于 AWS SSO 上下文，可能需要重新登录 SSO 会话。',
        ],
        authCommand: { label: '如果此上下文使用 AWS SSO，请刷新凭据：', command: `aws sso login${profileFlag}` },
      }
      if (parsed.region && allShellSafe(parsed.clusterName, parsed.region)) {
        result.fallbackCommand = {
          label: '如果仍无效，请刷新集群凭据：',
          command: `aws eks update-kubeconfig --name ${parsed.clusterName} --region ${parsed.region}${profileFlag}`,
        }
      }
      return result
    }
    case 'AKS':
      return {
        title: 'AKS 身份验证失败',
        hints: ['Radar 无法获取此上下文的 Azure 凭据。'],
        authCommand: { label: '刷新 Azure 凭据：', command: 'az login' },
        fallbackCommand: { label: '如果仍无效，请刷新集群凭据：', command: 'az aks get-credentials --name <cluster> --resource-group <rg>', runnable: false },
      }
    default:
      return {
        title: '身份验证失败',
        hints: [
          'Radar 无法获取此上下文的 Kubernetes 凭据',
          '请向云服务提供商重新进行身份验证后重试',
        ],
      }
  }
}

export function getAuthRejectedHints(context: string, awsProfile?: string): AuthHints {
  const parsed = parseContextName(context)

  switch (parsed.provider) {
    case 'EKS': {
      const profileFlag = awsProfileFlag(awsProfile)
      const result: AuthHints = {
        title: 'EKS 无法验证此请求',
        hints: [
          'EKS 返回 HTTP 401，因此 Kubernetes 无法验证此请求。',
          'AWS 凭据可能缺失、已过期或已撤销；也可能是 IAM 主体尚未通过 EKS access entry 或集群旧版 aws-auth 配置完成映射。',
          '如果凭据仍有效，请联系集群管理员核实此上下文所用 IAM 主体的映射。',
          profileFlag
            ? '以下命令使用此上下文 kubeconfig exec 块指定的 AWS profile。如果其中也指定了 --role-arn，请改为检查该角色。'
            : '此诊断使用终端当前的 AWS profile。如果 kubeconfig exec 块指定了 AWS_PROFILE 或 --role-arn，请改用相应 profile 或角色。',
          'API 和 API_AND_CONFIG_MAP 模式使用 access entry；CONFIG_MAP 模式使用 aws-auth ConfigMap。',
        ],
        fallbackCommand: {
          label: profileFlag ? '如果此 profile 使用 AWS SSO，请重新登录后重试：' : '如果此上下文使用当前 AWS SSO profile，请重新登录后重试：',
          command: `aws sso login${profileFlag}`,
        },
      }
      if (parsed.region && allShellSafe(parsed.clusterName, parsed.region)) {
        result.authCommand = {
          label: profileFlag
            ? '检查指定 AWS profile 的调用主体和身份验证模式：'
            : '检查终端当前 AWS profile 的调用主体和身份验证模式：',
          command: `aws sts get-caller-identity${profileFlag} && aws eks describe-cluster --name ${parsed.clusterName} --region ${parsed.region}${profileFlag} --query cluster.accessConfig.authenticationMode --output text`,
        }
        // A diagnostic, not a re-auth — the "Authenticate in terminal" button
        // would misrepresent what running it does.
        result.hideAuthButton = true
      }
      return result
    }
    case 'GKE': {
      const result: AuthHints = {
        title: 'GKE 无法验证此请求',
        hints: [
          'GKE 返回 HTTP 401，因此 Kubernetes 无法验证此请求。',
          '凭据或身份验证插件可能缺失或配置错误，OAuth token 也可能已过期或撤销。',
        ],
        authCommand: { label: '刷新 Google Cloud 凭据：', command: 'gcloud auth login' },
      }
      if (parsed.region && parsed.account && allShellSafe(parsed.clusterName, parsed.region, parsed.account)) {
        const isZone = /^[a-z]+-[a-z]+\d+-[a-z]$/.test(parsed.region)
        const flag = isZone ? '--zone' : '--region'
        result.fallbackCommand = {
          label: '如果仍无效，请刷新集群凭据：',
          command: `gcloud container clusters get-credentials ${parsed.clusterName} ${flag} ${parsed.region} --project ${parsed.account}`,
        }
      }
      return result
    }
    case 'AKS':
      return {
        title: 'AKS 无法验证此请求',
        hints: [
          'AKS 返回 HTTP 401，因此 Kubernetes 无法验证此请求。',
          '凭据可能缺失、已过期或已撤销——重新进行身份验证通常可以解决此问题。',
        ],
        authCommand: { label: '刷新 Azure 凭据：', command: 'az login' },
        fallbackCommand: { label: '如果仍无效，请刷新集群凭据：', command: 'az aks get-credentials --name <cluster> --resource-group <rg>', runnable: false },
      }
    default:
      return {
        title: 'Kubernetes 无法验证此请求',
        hints: [
          'Kubernetes API 返回 HTTP 401，因此无法验证此请求。',
          'kubeconfig 用户可能未提供凭据，或凭据已过期或撤销。',
          '如果集群显式映射身份，此上下文所用主体可能尚未完成映射。',
        ],
      }
  }
}

function getAuthPluginStuckHints(): AuthHints {
  return {
    title: '凭据插件未响应',
    hints: [
      '此 kubeconfig 配置的凭据命令未能在时限内返回。',
      '请检查云服务商 CLI 及其身份提供商或网络依赖是否正常响应。',
      'Radar 会在后台持续检查，并在凭据命令恢复后重新连接。',
    ],
  }
}

function getTimeoutHints(context: string, awsProfile?: string): AuthHints | null {
  const parsed = parseContextName(context)
  const baseHints = [
    'Kubernetes API 未能在时限内响应。',
    '请检查 VPN、防火墙规则以及集群端点是否可访问。',
  ]

  switch (parsed.provider) {
    case 'GKE': {
      const result: AuthHints = {
        title: '连接超时',
        hints: [...baseHints, '如果端点可访问，可能需要刷新 Google Cloud 凭据。'],
        authCommand: { label: '如果网络访问正常，请刷新 Google Cloud 凭据：', command: 'gcloud auth login' },
      }
      if (parsed.region && parsed.account && allShellSafe(parsed.clusterName, parsed.region, parsed.account)) {
        const isZone = /^[a-z]+-[a-z]+\d+-[a-z]$/.test(parsed.region)
        const flag = isZone ? '--zone' : '--region'
        result.fallbackCommand = {
          label: '如果仍无效，请刷新集群凭据：',
          command: `gcloud container clusters get-credentials ${parsed.clusterName} ${flag} ${parsed.region} --project ${parsed.account}`,
        }
      }
      return result
    }
    case 'EKS': {
      const profileFlag = awsProfileFlag(awsProfile)
      const result: AuthHints = {
        title: '连接超时',
        hints: [...baseHints, '如果端点可访问，可能需要刷新 AWS 凭据或 SSO。'],
        authCommand: { label: '如果此上下文使用 AWS SSO 且网络访问正常，请刷新凭据：', command: `aws sso login${profileFlag}` },
      }
      if (parsed.region && allShellSafe(parsed.clusterName, parsed.region)) {
        result.fallbackCommand = {
          label: '如果仍无效，请刷新集群凭据：',
          command: `aws eks update-kubeconfig --name ${parsed.clusterName} --region ${parsed.region}${profileFlag}`,
        }
      }
      return result
    }
    case 'AKS':
      return {
        title: '连接超时',
        hints: [...baseHints, '如果端点可访问，可能需要刷新 Azure 凭据。'],
        authCommand: { label: '如果网络访问正常，请刷新 Azure 凭据：', command: 'az login' },
        fallbackCommand: { label: '如果仍无效，请刷新集群凭据：', command: 'az aks get-credentials --name <cluster> --resource-group <rg>', runnable: false },
      }
    default:
      return null
  }
}

const errorHints: Record<string, { title: string; hints: string[] }> = {
  config: {
    title: 'kubeconfig 配置问题',
    hints: [
      'Radar 无法为此上下文加载可用的 kubeconfig',
      '如果文件存在，请查看本地 Radar 日志以了解具体解析或加载失败原因',
      '如果未配置 kubeconfig，Radar 会检查 ~/.kube/config；如需指定其他位置，请设置 KUBECONFIG 或传入 --kubeconfig <path>',
    ],
  },
  rbac: {
    title: '权限不足',
    hints: [
      '你的用户账号可以连接，但缺少所需的 RBAC 权限',
      '请联系集群管理员授予包含 list/watch 权限的 ClusterRole',
      '只读访问通常使用内置的 "view" ClusterRole 即可',
      '你也可以尝试：kubectl auth can-i --list',
    ],
  },
  network: {
    title: '网络不可达',
    hints: [
      '当前网络可能无法访问集群',
      '检查是否需要连接 VPN',
      '确认防火墙规则允许访问',
      '确认集群正在运行',
    ],
  },
  tls: {
    title: '证书错误',
    hints: [
      'Radar 已连接到 Kubernetes API，但 TLS 握手失败',
      '如果错误提到 "bad certificate" 或 "certificate required"，说明集群拒绝了 Radar 客户端证书——证书可能已过期（kubeadm 证书有效期为一年）。请续期（例如运行 kubeadm certs renew）或重新下载 kubeconfig',
      '否则请检查 kubeconfig 中集群服务器主机名和 certificate-authority 设置',
      '如果此集群有意使用私有 CA，请刷新此上下文的 kubeconfig',
    ],
  },
  timeout: {
    title: '连接超时',
    hints: [
      '集群响应时间过长',
      '集群可能负载过高',
      '网络延迟可能过高',
      '请重试或检查集群健康状况',
    ],
  },
  unknown: {
    title: '连接失败',
    hints: [
      '请检查 kubeconfig 是否有效',
      '请确认集群端点正确',
      '尝试切换到其他上下文',
    ],
  },
}

export function CopyableCommand({ command, onRunInTerminal }: { command: string; onRunInTerminal?: (command: string) => void }) {
  const [copied, setCopied] = useState(false)
  const commandParts = command.split(/(\s+)/)

  const handleCopy = () => {
    navigator.clipboard.writeText(command).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }).catch(() => {
      // Clipboard API may be unavailable (e.g., non-HTTPS context)
    })
  }

  return (
    <div className="mt-2 flex items-center gap-2 bg-theme-elevated border border-theme-border rounded-md px-3 py-2 group">
      <code className="text-xs font-mono text-theme-text-primary flex-1 min-w-0 select-all whitespace-pre-wrap break-normal">
        {commandParts.map((part, index) => (
          /\s+/.test(part) ? part : <span key={index} className="inline-block whitespace-nowrap">{part}</span>
        ))}
      </code>
      {onRunInTerminal && (
        <Tooltip content="在终端中运行" wrapperClassName="shrink-0">
          <button
            type="button"
            onClick={() => onRunInTerminal(command)}
            aria-label="在终端中运行命令"
            className="shrink-0 text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
          >
            <TerminalSquare className="w-3.5 h-3.5" />
          </button>
        </Tooltip>
      )}
      <Tooltip content="复制到剪贴板" wrapperClassName="shrink-0">
        <button
          type="button"
          onClick={handleCopy}
          aria-label="复制命令到剪贴板"
          className="shrink-0 text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
        >
          {copied ? (
            <Check className="w-3.5 h-3.5 text-green-400" />
          ) : (
            <Copy className="w-3.5 h-3.5" />
          )}
        </button>
      </Tooltip>
    </div>
  )
}

export function selectConnectionHints(errorType: string | undefined, context: string, originalContext?: string, awsProfile?: string): AuthHints | null {
  const parsedContext = originalContext || context
  switch (errorType) {
    case 'auth':
      return getAuthHints(parsedContext, awsProfile)
    case 'auth-rejected':
      return getAuthRejectedHints(parsedContext, awsProfile)
    case 'auth-plugin-stuck':
      return getAuthPluginStuckHints()
    case 'timeout':
      return getTimeoutHints(parsedContext, awsProfile)
    default:
      return null
  }
}

export function ConnectionErrorView({ connection, onRetry, isRetrying }: ConnectionErrorViewProps) {
  // For auth errors, generate context-aware hints with a specific re-auth command
  const isAuth = connection.errorType === 'auth'
  const isAuthRejected = connection.errorType === 'auth-rejected'
  const isAuthPluginStuck = connection.errorType === 'auth-plugin-stuck'
  const isAuthError = isAuth || isAuthRejected || isAuthPluginStuck
  const { data: contexts, isLoading: contextsLoading } = useContexts()
  const matchedContext = contexts?.find((context) => context.name === connection.context)
  const originalContext = matchedContext?.originalName
  const awsProfile = contextsLoading ? undefined : matchedContext?.awsProfile
  const commandInfo = selectConnectionHints(connection.errorType, connection.context || '', originalContext, awsProfile)
  const errorInfo = commandInfo || errorHints[connection.errorType || 'unknown'] || errorHints.unknown
  const openLocalTerminal = useOpenLocalTerminal()
  const { data: authMe } = useAuthMe()
  const { localTerminal } = useCapabilitiesContext()
  const rawErrorDefaultOpen = !connection.errorType || connection.errorType === 'unknown'
  const [showRawError, setShowRawError] = useState(rawErrorDefaultOpen)

  useEffect(() => {
    setShowRawError(rawErrorDefaultOpen)
  }, [connection.error, rawErrorDefaultOpen])

  // The local terminal is only available in unauthenticated local mode, but
  // authMe may still be loading when the capability response arrives.
  const retryCmd = `curl -s -X POST http://${window.location.host}${apiUrl('/connection/retry')} > /dev/null`

  const handleAuthInTerminal = () => {
    if (!commandInfo?.authCommand) return
    const cmd = authMe?.authEnabled === false
      ? `${commandInfo.authCommand.command} && ${retryCmd}`
      : commandInfo.authCommand.command
    openLocalTerminal({
      initialCommand: cmd,
      title: '身份验证',
    })
  }

  const handleRunInTerminal = (command: string) => {
    openLocalTerminal({ initialCommand: command, title: '身份验证' })
  }

  return (
    <div className="flex-1 flex items-start justify-center pt-12 px-8">
      <div className="max-w-xl w-full">
        <div className="flex flex-col items-center text-center">
          <div className="w-14 h-14 rounded-full bg-red-500/10 flex items-center justify-center mb-5">
            <ServerOff className="w-8 h-8 text-red-400" />
          </div>

          <h2 className="text-xl font-semibold text-theme-text-primary mb-2">
            {connection.errorType === 'config' ? '无法加载集群配置' : '无法连接到集群'}
          </h2>

          <div className="mb-6 space-y-1">
            <p className="text-sm text-theme-text-secondary inline-flex items-center gap-1.5">
              上下文：{connection.context ? (
                <ClusterName name={connection.context} />
              ) : (
                <span className="inline-code">(none)</span>
              )}
            </p>

            {connection.clusterName && (
              <p className="text-sm text-theme-text-secondary">
                集群：<span className="inline-code">{connection.clusterName}</span>
              </p>
            )}
          </div>

          <div className="w-full bg-theme-surface border border-theme-border rounded-lg p-4 mb-5 text-left">
            <h3 className="text-sm font-medium text-theme-text-primary mb-2">
              {errorInfo.title}
            </h3>
            <ul className="text-sm text-theme-text-secondary space-y-1">
              {errorInfo.hints.map((hint, i) => (
                <li key={i} className="flex items-start gap-2">
                  <span className="text-theme-text-tertiary mt-0.5">-</span>
                  <span>{hint}</span>
                </li>
              ))}
            </ul>
            {commandInfo?.authCommand && (
              <div className="mt-3">
                <p className="text-xs text-theme-text-tertiary">{commandInfo.authCommand.label}</p>
                <CopyableCommand command={commandInfo.authCommand.command} onRunInTerminal={!localTerminal || commandInfo.authCommand.runnable === false ? undefined : handleRunInTerminal} />
                {localTerminal && isAuthError && !commandInfo?.hideAuthButton && commandInfo.authCommand.runnable !== false && (
                  <button
                    onClick={handleAuthInTerminal}
                    className="mt-3 w-full inline-flex items-center justify-center gap-2 px-3 py-2 text-xs font-medium btn-brand rounded-md"
                  >
                    <TerminalSquare className="w-3.5 h-3.5" />
                  在终端中完成身份验证
                  </button>
                )}
              </div>
            )}
            {commandInfo?.fallbackCommand && (
              <div className="mt-4 pt-3 border-t border-theme-border/50">
                <p className="text-xs text-theme-text-tertiary">{commandInfo.fallbackCommand.label}</p>
                <CopyableCommand command={commandInfo.fallbackCommand.command} onRunInTerminal={!localTerminal || commandInfo.fallbackCommand.runnable === false ? undefined : handleRunInTerminal} />
              </div>
            )}
            {connection.error && (
              <div className="mt-4 pt-3 border-t border-theme-border/50">
                <button
                  type="button"
                  aria-expanded={showRawError}
                  aria-controls="connection-raw-error"
                  onClick={() => setShowRawError((open) => !open)}
                  className="flex items-center gap-1 text-xs font-medium text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
                >
                  <CollapseChevron open={showRawError} className="h-3.5 w-3.5" />
                  原始错误
                </button>
                <Collapse open={showRawError} id="connection-raw-error">
                  <div className="mt-2 bg-theme-elevated border border-theme-border rounded-md p-3 overflow-auto max-h-32">
                    <code className="text-xs text-theme-text-tertiary font-mono whitespace-pre-wrap break-words">
                      {connection.error}
                    </code>
                  </div>
                </Collapse>
              </div>
            )}
          </div>

          <div className="flex items-center gap-5">
            <button
              onClick={onRetry}
              disabled={isRetrying}
              className="inline-flex items-center gap-2 px-4 py-2 btn-brand rounded-lg"
            >
              {isRetrying ? (
                <>
                  <Loader2 className="w-4 h-4 animate-spin" />
                  Connecting...
                </>
              ) : (
                <>
                  <RefreshCw className="w-4 h-4" />
                  重试连接
                </>
              )}
            </button>

            {connection.context && <ContextSwitcher triggerName="切换上下文" />}
          </div>

          {isAuthError && (
            <p className="mt-4 text-xs text-theme-text-tertiary">
              {isAuthRejected
                ? 'Radar 会在后台重新检查并自动获取访问权限变更。点击“重试连接”可立即检查。'
                : isAuthPluginStuck
                  ? 'Radar 会在后台重新检查，并在凭据插件响应后重新连接。点击“重试连接”可立即检查。'
                  : 'Radar 会在后台重新检查，并在凭据刷新后重新连接。点击“重试连接”可立即检查。'}
            </p>
          )}
        </div>
      </div>
    </div>
  )
}
