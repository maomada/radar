import { useEffect, useRef, useState, useId, type ReactNode } from 'react'
import { Check, Info, Loader2, Plug, Plus, X } from 'lucide-react'
import { Collapse, Input } from '@skyhook-io/k8s-ui'
import { Tooltip } from '../ui/Tooltip'
import { prometheusHeadersFromRows } from './settings-state'
import { ConnectionHeadersEditor, type HeaderOperation } from './ConnectionHeadersEditor'
import { FormSaveActions, type FormFeedback } from './FormSaveActions'

export interface PrometheusApplyResult { connected: boolean; address?: string; error?: string }

type ApplyState =
  | { status: 'idle' }
  | { status: 'applying' }
  | { status: 'connected'; address: string }
  | { status: 'unreachable'; error: string } // persisted, but the probe failed
  | { status: 'failed'; error: string }       // request itself failed — nothing saved

type HeaderRow = { key: string; value: string }

export function PrometheusConnectionForm({
  local,
  value,
  onChange,
  configuredHeaderKeys,
  serverManaged,
  headersManaged,
  urlFromFlag,
  onApplied,
  onApply,
  onDirtyChange,
  onApplyOperations,
  environmentHeaderKeys = [],
  dirty = false,
  onDiscard,
  connectionAction,
  feedback,
}: {
  local: boolean
  value: string
  onChange: (value: string) => void
  configuredHeaderKeys: string[]
  serverManaged: boolean
  headersManaged: boolean
  urlFromFlag: boolean
  onApplied?: (url: string) => void
  onApply: (url: string, headers?: Record<string, string>) => Promise<PrometheusApplyResult>
  onDirtyChange?: (dirty: boolean) => void
  onApplyOperations?: (url: string, operations: HeaderOperation[]) => Promise<PrometheusApplyResult | null>
  environmentHeaderKeys?: string[]
  dirty?: boolean
  onDiscard?: () => void
  connectionAction?: ReactNode
  feedback?: FormFeedback
}) {
  const mounted = useRef(true)
  const urlId = useId()
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const [apply, setApply] = useState<ApplyState>({ status: 'idle' })
  // null = not editing headers (preserve what's stored). A non-null array means
  // the user opened the editor; see handleApply for how rows become a
  // replacement (values are write-only, so the server never sends them back).
  const [headerRows, setHeaderRows] = useState<HeaderRow[] | null>(null)
  // Show the server's configured header keys, but let a successful apply override
  // optimistically (config isn't refetched). Derived from the prop — not a
  // mount-time snapshot — so it stays correct as config loads asynchronously.
  const [appliedKeys, setAppliedKeys] = useState<string[] | null>(null)
  const [operations, setOperations] = useState<HeaderOperation[]>([])
  const storedKeys = appliedKeys ?? configuredHeaderKeys
  useEffect(() => { onDirtyChange?.(headerRows !== null || operations.some(operation => operation.action !== 'keep')) }, [headerRows, operations, onDirtyChange])

  const clearStatus = () => {
    if (apply.status !== 'applying') setApply({ status: 'idle' })
  }

  // Footer Reset (and any external edit) clears the URL field without a keystroke;
  // drop a stale "Connected"/"Saved" status so it doesn't describe an emptied field.
  useEffect(() => {
    setApply((s) => (s.status === 'idle' || s.status === 'applying' ? s : { status: 'idle' }))
  }, [value])

  const handleApply = async () => {
    setApply({ status: 'applying' })
    // Decide what to do with headers. undefined = leave them untouched. Only send
    // a replacement when the editor has real content, or {} when the user emptied
    // every row (explicit clear) — blank in-progress rows must NOT wipe stored
    // secrets just because the editor happens to be open for a URL-only change.
    try {
      const editedHeaders = prometheusHeadersFromRows(headerRows)
      const editedOperations = operations.filter(operation => operation.key.trim() || operation.value)
      if (onApplyOperations) {
        const names = new Set<string>()
        for (const operation of editedOperations) {
          const name = operation.key.trim().toLowerCase()
          if (!name) throw new Error('Enter a name for each header with a value.')
          if (names.has(name)) throw new Error(`Header names must be unique: ${operation.key.trim()}.`)
          if (operation.action === 'set' && !operation.value) throw new Error(`Enter a value for ${operation.key.trim()}.`)
          names.add(name)
        }
      }
      const data = onApplyOperations ? await onApplyOperations(value.trim(), editedOperations) : await onApply(value.trim(), editedHeaders)
      if (!mounted.current) return
      if (!data) { setApply({ status: 'idle' }); return }
      onApplied?.(value.trim())
      if (editedHeaders !== undefined) {
        setAppliedKeys(Object.keys(editedHeaders).sort())
      }
      if (headerRows !== null) {
        setHeaderRows(null)
      }
      setOperations([])
      if (data.connected) {
        setApply({ status: 'connected', address: data.address || value.trim() })
      } else {
        setApply({ status: 'unreachable', error: data.error || 'not reachable' })
      }
    } catch (err) {
      if (mounted.current) setApply({ status: 'failed', error: err instanceof Error ? err.message : String(err) })
    }
  }

  return (
    <div>
      <div className="mb-4 space-y-1">
        <p className="text-sm text-theme-text-secondary">
          连接 Prometheus 兼容后端，例如 Prometheus、VictoriaMetrics、Thanos 或 Grafana Mimir。
        </p>
      </div>
      <div className="flex flex-wrap items-center gap-1 mb-1">
        <label htmlFor={urlId} className="text-sm font-medium text-theme-text-primary">指标后端 URL</label>
        <Tooltip content="URL 必须可由 Radar 服务器访问；仅浏览器可以访问还不够。请包含后端路径前缀，但不要包含 /api/v1/query。可用图表取决于已收集的指标。">
          <button type="button" aria-label="指标 URL 帮助" className="text-theme-text-tertiary"><Info className="w-3.5 h-3.5" /></button>
        </Tooltip>
      </div>
      <p className="text-xs text-theme-text-tertiary mb-1">
        留空以自动发现此集群中的后端。
      </p>
      <div className="flex items-center gap-2">
        <Input
          id={urlId}
          value={value}
          disabled={apply.status === 'applying'}
          onChange={(e) => onChange(e.target.value)}
          placeholder="http://prometheus-server.monitoring:9090"
          className="flex-1 min-w-0 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
        />
      </div>
      {connectionAction}
      {!onApplyOperations && <p className="mt-2 text-xs text-theme-text-tertiary">
        {local ? '仅应用于此集群。' : '更改将影响此 Radar 安装。若要在 Pod 替换后保留配置，请使用部署设置。'}
      </p>}
      {value.startsWith('http://') && (configuredHeaderKeys.length > 0 || operations.some(operation => operation.action === 'set')) && (
        <p className="mt-2 text-xs text-warning-text">请求头将通过未加密的 HTTP 传输。在可信的私有网络之外，请优先使用 HTTPS。</p>
      )}
      {serverManaged && (
        <p className="mt-2 text-xs text-theme-text-secondary">
          服务器由启动配置控制。若要设置或更改服务器，
          {local ? '请更新启动标志或环境变量引用，然后重启 Radar。' : '请更新部署配置（例如 Helm values），然后重启 Radar。'}
        </p>
      )}
      {urlFromFlag && (
        <p className="mt-1 text-xs text-theme-text-secondary">
          路径更改仅在重启前生效；重启后将恢复启动时提供的 URL。
        </p>
      )}

      {onApplyOperations ? <ConnectionHeadersEditor keys={configuredHeaderKeys} environmentKeys={environmentHeaderKeys} value={operations} onChange={setOperations} /> : <section className="mt-5 border-t border-theme-border pt-4">
        <h4 className="text-sm font-medium text-theme-text-primary mb-2">身份验证请求头</h4>
        <p className="text-xs text-theme-text-tertiary mb-3">
          可选的身份验证或租户请求头，例如 Authorization 或 X-Scope-OrgID。需要填写后端 URL。
          {!headersManaged && storedKeys.length > 0 && ' 更换服务器时需要替换或清除已保存的请求头。'}
        </p>
        {headerRows === null ? (
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="min-w-0 break-words text-xs text-theme-text-tertiary">
              {storedKeys.length > 0
                ? <><span className="text-theme-text-secondary">{storedKeys.join(', ')}</span>（值已隐藏）</>
                : '未配置请求头'}
            </span>
            {!headersManaged && <button
              onClick={() => { setHeaderRows([{ key: '', value: '' }]); clearStatus() }}
              disabled={apply.status === 'applying'}
              className="shrink-0 text-xs font-medium text-accent-text hover:underline"
            >
              {storedKeys.length > 0 ? '修改请求头' : '添加身份验证请求头'}
            </button>}
          </div>
        ) : (
          <div className="rounded-md border border-theme-border bg-theme-elevated/40 p-2.5 space-y-2">
            {headerRows.map((row, i) => (
              <div key={i} className="flex items-center gap-2">
                <Input
                  value={row.key}
                  disabled={apply.status === 'applying'}
                  onChange={(e) => {
                    setHeaderRows((rows) => rows!.map((r, j) => j === i ? { ...r, key: e.target.value } : r))
                    clearStatus()
                  }}
                  placeholder="请求头（例如 Authorization）"
                  className="flex-1 min-w-0 px-2.5 py-1.5 text-xs bg-theme-base border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                />
                <input
                  type="password"
                  value={row.value}
                  disabled={apply.status === 'applying'}
                  onChange={(e) => {
                    setHeaderRows((rows) => rows!.map((r, j) => j === i ? { ...r, value: e.target.value } : r))
                    clearStatus()
                  }}
                  placeholder="值（例如 Bearer …）"
                  className="flex-1 min-w-0 px-2.5 py-1.5 text-xs bg-theme-base border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                />
                <Tooltip content="移除请求头" wrapperClassName="shrink-0">
                  <button
                    onClick={() => setHeaderRows((rows) => rows!.filter((_, j) => j !== i))}
                    disabled={apply.status === 'applying'}
                    className="p-1 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover rounded"
                  >
                    <X className="w-3.5 h-3.5" />
                  </button>
                </Tooltip>
              </div>
            ))}
            <div className="flex items-center justify-between gap-2">
              <button
                onClick={() => setHeaderRows((rows) => [...rows!, { key: '', value: '' }])}
                disabled={apply.status === 'applying'}
                className="flex items-center gap-1 text-xs font-medium text-accent-text hover:underline"
              >
                <Plus className="w-3 h-3" /> Add header
              </button>
              <button
                onClick={() => { setHeaderRows(null); clearStatus() }}
                disabled={apply.status === 'applying'}
                className="text-xs text-theme-text-tertiary hover:text-theme-text-primary"
              >
                Cancel
              </button>
            </div>
            <p className="text-xs text-theme-text-tertiary">
              Saved when you click Apply now. Entered headers replace all stored
              ones — values are hidden, so re-enter any you want to keep. Leave
              all rows blank to keep existing headers unchanged. To remove all saved
              headers, remove every row and click Apply now.
            </p>
          </div>
        )}
        {headersManaged ? (
          <p className="mt-2 text-xs text-theme-text-secondary">
            Headers are controlled by startup configuration. Change them at their source and restart Radar.
          </p>
        ) : storedKeys.length > 0 && (
          <button
            onClick={() => { setHeaderRows([]); clearStatus() }}
            disabled={apply.status === 'applying'}
            className="mt-2 text-xs text-theme-text-secondary hover:underline"
          >
            Remove saved headers
          </button>
        )}
        {headerRows?.length === 0 && (
          <p className="mt-1 text-xs text-warning-text">点击“立即应用”后将移除这些请求头。</p>
        )}
      </section>}
      <div className="mt-4 space-y-1">
        {onDiscard ? <FormSaveActions dirty={dirty} busy={apply.status === 'applying'} onSave={() => void handleApply()} onDiscard={onDiscard} feedback={feedback} error={apply.status === 'failed' ? apply.error : undefined} /> : <Tooltip content="保存并应用此连接，然后检查是否可访问。应用后会清除工作负载范围覆盖，并恢复自动身份匹配。">
          <button
            type="button"
            onClick={handleApply}
            disabled={apply.status === 'applying'}
            className="flex items-center gap-1.5 px-3 py-2 text-xs font-medium btn-brand rounded-md disabled:opacity-50"
          >
            {apply.status === 'applying'
              ? <Loader2 className="w-3.5 h-3.5 animate-spin" />
              : <Plug className="w-3.5 h-3.5" />}
            Apply now
          </button>
        </Tooltip>}
        {!onDiscard && <Collapse open={apply.status === 'failed' || (!onApplyOperations && apply.status !== 'applying')}>
        {onApplyOperations && (apply.status === 'connected' || apply.status === 'unreachable') ? null : apply.status === 'connected' ? (
          <p role="status" className="flex items-center gap-1 text-xs text-theme-text-secondary">
            <Check className="w-3 h-3 shrink-0 text-[var(--color-success-dark)] dark:text-[var(--color-success-light)]" />
            Connected to {apply.address} — applied, no restart needed
          </p>
        ) : apply.status === 'unreachable' ? (
          <p role="status" className="text-xs text-warning-text">Saved, but not reachable: {apply.error}</p>
        ) : apply.status === 'failed' ? (
          <p role="alert" className="text-xs text-semantic-error">Couldn't apply: {apply.error}</p>
        ) : !onApplyOperations ? (
          <p className="text-xs text-theme-text-tertiary">保存并应用设置后再检查连接。</p>
        ) : null}
        </Collapse>}
      </div>
    </div>
  )
}
