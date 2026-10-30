import { useEffect, useRef, useState } from 'react'
import { Badge, ConfirmDialog, Disclosure } from '@skyhook-io/k8s-ui'
import {
  apiUrl,
  getApiBase,
  getAuthHeaders,
  getCredentialsMode,
} from '../../api/config'
import {
  integrationNames,
  type ConnectionResponse,
  type IntegrationProfiles,
  type StoredConnection,
} from './LocalConnectionSettings'
import type { SettingsSectionId } from './settings-state'

// Dismissal reports only that the global notice flag is set; replacing the
// profiles from a late response could overwrite a newer context's settings.
export function PreviousIntegrationSettingsNotice({ profiles, onNavigate, onDismissed }: {
  profiles: IntegrationProfiles
  onNavigate: (section: SettingsSectionId) => void
  onDismissed: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const available = (['metrics', 'argocd', 'cost'] as const).filter(kind => {
    const legacy = profiles[kind].legacy
    return profiles[kind].state === 'auto' && legacy && !legacy.error && !legacy.noticeDismissed
  })
  if (available.length === 0) return null
  const dismiss = async () => {
    setBusy(true)
    setError('')
    try {
      const response = await fetch(apiUrl('/integrations/connections'), {
        method: 'PUT',
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify({ action: 'dismiss_previous_notice' }),
      })
      const data = (await response.json().catch(() => ({}))) as ConnectionResponse
      if (!response.ok) throw new Error(data.error || `Settings request failed (${response.status})`)
      onDismissed()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section aria-label="之前的集成设置" className="mt-3 rounded-lg border border-theme-border bg-accent-muted p-3">
      <h4 className="text-sm font-medium text-theme-text-primary">集成设置现按集群保存</h4>
      <p className="mt-1 text-xs text-theme-text-secondary">
        之前的全局设置不再自动应用。如需在此集群使用，请在对应标签页选择{' '}
        <span className="font-medium">复制设置自… → 之前的全局设置</span>。
      </p>
      <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2">
        {available.map(kind => (
          <button key={kind} type="button" className="text-xs text-accent-text hover:underline"
            onClick={() => onNavigate(kind === 'metrics' ? 'prometheus' : kind)}>
            查看 {integrationNames[kind]}
          </button>
        ))}
        <button type="button" disabled={busy} className="text-xs text-theme-text-secondary hover:underline disabled:opacity-50"
          onClick={() => void dismiss()}>
          不再显示
        </button>
      </div>
      {error && <p role="alert" className="mt-2 text-xs text-warning-text">{error}</p>}
    </section>
  )
}

export const integrationSettingsByClusterId = 'integration-settings-by-cluster'

// Switching to auto-discovery keeps an empty record, so a record alone doesn't
// mean anything is saved. An invalid record disables the integration instead.
function integrationStatus(use: StoredConnection) {
  if (use.error || use.mode !== 'auto' || use.url || use.secretSet || use.clusterId) return 'saved'
  return use.integration === 'cost' ? '自动' : '自动发现'
}

export function LocalConfigurationDetails({ onNavigate }: { onNavigate: (section: SettingsSectionId) => void }) {
  return (
    <div className="mt-5 border-t border-theme-border pt-4">
      <Disclosure
        summary="配置文件"
        summaryClassName="text-sm font-medium text-theme-text-primary"
      >
        <div className="space-y-3 pt-2 text-xs text-theme-text-secondary">
          <p>
            CLI 与 Desktop 在此设备上共用 <code>~/.radar/</code> 中的这些文件。
          </p>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
            <dt className="font-mono text-theme-text-primary">config.json</dt>
            <dd>
              启动默认值，例如浏览器、端口和时间线存储。命令行标志优先。
            </dd>
            <dt className="font-mono text-theme-text-primary">settings.json</dt>
            <dd>本地偏好设置、审计设置和 OCI 注册表来源。</dd>
            <dt className="font-mono text-theme-text-primary">clusters.json</dt>
            <dd>
              Metrics、Argo CD 和 Cost 的连接及凭据，按 kubeconfig 条目分别保存。{' '}
              <button
                type="button"
                className="text-accent-text hover:underline"
                onClick={() => {
                  onNavigate('connection')
                  requestAnimationFrame(() => document.getElementById(integrationSettingsByClusterId)?.focus())
                }}
              >
                查看各集群的集成设置
              </button>
            </dd>
          </dl>
          <p className="text-theme-text-tertiary">
            凭据以受文件权限保护的明文保存，并未加密。其他进程所做的连接更改将在下一次集成操作时生效。
          </p>
        </div>
      </Disclosure>
    </div>
  )
}

export function SavedClusterConnections({
  active,
  profiles,
  onChange,
  onBusyChange,
}: {
  active: boolean
  profiles: IntegrationProfiles
  onChange: (profiles: IntegrationProfiles) => void
  onBusyChange: (busy: boolean) => void
}) {
  const [uses, setUses] = useState<StoredConnection[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [version, setVersion] = useState(0)
  const [selected, setSelected] = useState<StoredConnection | null>(null)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const request = useRef<AbortController | null>(null)
  const summary = useRef<HTMLHeadingElement>(null)
  const confirmationError = useRef<HTMLParagraphElement>(null)
  // The removed entry's button is gone, so the dialog cannot return focus to it.
  const focusSummary = useRef(false)

  useEffect(() => {
    if (selected || busy || !focusSummary.current) return
    focusSummary.current = false
    summary.current?.focus()
  }, [selected, busy])

  useEffect(() => {
    if (selected && error && !busy) confirmationError.current?.focus()
  }, [selected, error, busy])

  useEffect(() => {
    if (!active) return
    const controller = new AbortController()
    const base = getApiBase()
    setLoading(true)
    setError('')
    void fetch(apiUrl('/integrations/connections'), {
      signal: controller.signal,
      credentials: getCredentialsMode(),
      headers: getAuthHeaders(),
    })
      .then(async (response) => {
        const data = (await response.json().catch(() => ({}))) as ConnectionResponse
        if (!response.ok)
          throw new Error(data.error || 'Could not load integration settings.')
        if (controller.signal.aborted || getApiBase() !== base) return
        setUses(data.connections)
      })
      .catch((error) => {
        if (!controller.signal.aborted)
          setError(error instanceof Error ? error.message : String(error))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [active, version])

  useEffect(
    () => () => {
      request.current?.abort()
      onBusyChange(false)
    },
    [onBusyChange],
  )

  const remove = async () => {
    if (!selected || request.current) return
    const controller = new AbortController()
    request.current = controller
    const base = getApiBase()
    setBusy(true)
    onBusyChange(true)
    setError('')
    try {
      const response = await fetch(apiUrl('/integrations/connections'), {
        method: 'PUT',
        signal: controller.signal,
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify({
          action: 'forget',
          kind: selected.integration,
          binding: selected.binding,
          sourceRevision: selected.revision,
          confirmRemoval: true,
        }),
      })
      const data = (await response.json().catch(() => ({}))) as ConnectionResponse
      if (controller.signal.aborted || getApiBase() !== base) return
      if (!response.ok)
        throw new Error(data.error || 'Could not remove the saved settings.')
      setUses(data.connections)
      setMessage(
        `Removed the saved ${integrationNames[selected.integration]} settings for ${selected.context}.`,
      )
      focusSummary.current = true
      setSelected(null)
      onChange(data.profiles)
    } catch (error) {
      if (!controller.signal.aborted)
        setError(error instanceof Error ? error.message : String(error))
    } finally {
      if (!controller.signal.aborted) {
        setBusy(false)
        onBusyChange(false)
      }
      if (request.current === controller) request.current = null
    }
  }

  const clusters = new Map<string, StoredConnection[]>()
  for (const use of uses) {
    const entries = clusters.get(use.binding) ?? []
    entries.push(use)
    clusters.set(use.binding, entries)
  }
  return (
    <section
      className="mt-5 space-y-3 border-t border-theme-border pt-4"
    >
      <h4
        ref={summary}
        id={integrationSettingsByClusterId}
        tabIndex={-1}
        className="text-sm font-semibold text-theme-text-primary outline-none"
      >
        Integration settings by cluster
      </h4>
      <div className="space-y-3 pt-2">
        <p className="text-xs text-theme-text-secondary">
          Metrics, Argo CD and Cost settings saved for each kubeconfig entry.
          Edit the current cluster’s in its own tab. Removing an entry applies
          immediately and changes nothing in Kubernetes or the backend.
        </p>
        {loading && (
          <p role="status" className="text-xs text-theme-text-tertiary">
            Loading integration settings…
          </p>
        )}
        {!loading && !error && clusters.size === 0 && (
          <p className="text-xs text-theme-text-tertiary">
            No integration settings saved yet.
          </p>
        )}
        {!loading &&
          Array.from(clusters, ([binding, entries]) => {
            const entry = entries[0]
            const current = Object.values(profiles).some(
              (profile) => profile.target.binding === binding,
            )
            return (
              <div key={binding} className="card-inner-lg space-y-2 text-xs">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium text-theme-text-primary">
                    {entry.context}
                  </span>
                  {current ? (
                    <Badge tone="note" size="sm">
                      Current cluster
                    </Badge>
                  ) : entry.availability === 'removed' ? (
                    <Badge tone="note" size="sm">
                      Not in kubeconfig
                    </Badge>
                  ) : entry.availability === 'unavailable' ? (
                    <Badge tone="note" size="sm">
                      Kubeconfig not loaded
                    </Badge>
                  ) : null}
                </div>
                <p className="break-all text-theme-text-tertiary">
                  {entry.source} · {entry.inFileName}
                </p>
                {!current && entry.availability === 'removed' && (
                  <p className="text-theme-text-secondary">
                    This kubeconfig entry is gone, but its integration settings
                    are still saved. To reuse one, select the new context and choose “Copy
                    settings from…” in that integration’s settings.
                  </p>
                )}
                {(current ||
                  entries.every((use) => use.availability === 'available')) && (
                  <p className="text-theme-text-secondary">
                    {entries
                      .map((use) => `${integrationNames[use.integration]}: ${integrationStatus(use)}`)
                      .join(' · ')}
                  </p>
                )}
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  {entries
                    .filter(
                      (use) => !current && use.availability !== 'available',
                    )
                    .map((use) => (
                      <div key={use.integration} className="flex items-center">
                        <button
                          type="button"
                          disabled={busy}
                          className="text-accent-text hover:underline disabled:opacity-50"
                          aria-label={`移除 ${use.context} 的已保存 ${integrationNames[use.integration]} 设置`}
                          onClick={() => {
                            setSelected(use)
                            setError('')
                            setMessage('')
                          }}
                        >
                          移除 {integrationNames[use.integration]}
                        </button>
                      </div>
                    ))}
                </div>
              </div>
            )
          })}
        {message && (
          <p role="status" className="text-xs text-theme-text-secondary">
            {message}
          </p>
        )}
        {error && !selected && (
          <p role="alert" className="text-xs text-warning-text">
            {error}{' '}
            <button
              type="button"
              className="underline"
              onClick={() => setVersion((value) => value + 1)}
            >
              重新加载集成设置
            </button>
          </p>
        )}
      </div>
      <ConfirmDialog
        open={!!selected}
        onClose={() => setSelected(null)}
        onConfirm={() => void remove()}
        title="移除已保存的设置？"
        message={
          selected
            ? `移除 ${selected.context} 的已保存 ${integrationNames[selected.integration]} 设置和凭据？其他已保存设置不受影响。不会从 Kubernetes 或后端删除任何内容。${selected.availability === 'unavailable' ? ' 此条目可能仍存在于其他 kubeconfig 中；移除也会影响使用该条目的 CLI 或 Desktop。' : ''}`
            : ''
        }
        confirmLabel="移除设置"
        isLoading={busy}
        confirmDisabled={!!error}
        showWarning={false}
      >
        {error && (
          <p
            ref={confirmationError}
            tabIndex={-1}
            role="alert"
            className="text-sm text-warning-text"
          >
            {error} 请取消并重新加载集成设置后再试。
          </p>
        )}
      </ConfirmDialog>
    </section>
  )
}
