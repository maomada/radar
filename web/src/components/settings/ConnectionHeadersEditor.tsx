import { Input } from '@skyhook-io/k8s-ui'

export interface HeaderOperation {
  key: string
  action: 'keep' | 'set' | 'clear'
  value?: string
}

export function ConnectionHeadersEditor({
  keys,
  environmentKeys,
  value,
  onChange
}: {
  keys: string[]
  environmentKeys: string[]
  value: HeaderOperation[]
  onChange: (value: HeaderOperation[]) => void
}) {
  const rows = value.length
    ? value
    : keys.map((key): HeaderOperation => ({ key, action: 'keep' }))
  const update = (index: number, patch: Partial<HeaderOperation>) =>
    onChange(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)))
  return (
    <section className="mt-4 space-y-2">
      <h4 className="text-sm font-medium text-theme-text-primary">
        身份验证请求头
      </h4>
      <p className="text-xs text-theme-text-tertiary">
        可选。除非替换或移除，否则已保存的值保持不变。
      </p>
      {rows.map((row, index) => {
        const saved = index < keys.length
        const environment = saved && environmentKeys.some(
          (key) => key.toLowerCase() === row.key.toLowerCase()
        )
        return (
          <div key={index} className="space-y-1">
            <div className="flex items-center gap-2">
              <Input
                aria-label={`第 ${index + 1} 个请求头名称`}
                value={row.key}
                disabled={saved}
                onChange={(e) => update(index, { key: e.target.value })}
                placeholder="请求头名称"
                  className="min-w-0 flex-1 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
              />
              <Input
                aria-label={`${row.key || '新请求头'}的值`}
                type="password"
                autoComplete="new-password"
                disabled={environment || row.action === 'clear'}
                value={row.value ?? ''}
                placeholder={
                  row.action === 'clear'
                    ? '将被移除'
                    : environment
                      ? '来自环境变量'
                      : saved
                        ? '已保存的值'
                        : '值'
                }
                onChange={(e) =>
                  update(index, {
                    action: saved && !e.target.value ? 'keep' : 'set',
                    value: e.target.value || undefined
                  })
                }
                  className="min-w-0 flex-1 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
              />
              {!environment && (
                <button
                  type="button"
                  className="text-xs text-accent-text hover:underline shrink-0"
                  aria-label={`${row.action === 'clear' ? '撤销移除' : '移除'} ${row.key || '新请求头'}`}
                  onClick={() =>
                    saved
                      ? update(index, {
                          action: row.action === 'clear' ? 'keep' : 'clear',
                          value: undefined
                        })
                      : onChange(rows.filter((_, i) => i !== index))
                  }
                >
                  {row.action === 'clear' ? '撤销' : '移除'}
                </button>
              )}
            </div>
            {environment && (
              <p className="text-xs text-theme-text-tertiary">
                环境变量引用 · 请在 clusters.json 中编辑此请求头。
              </p>
            )}
          </div>
        )
      })}
      <button
        type="button"
        className="text-xs text-accent-text hover:underline"
        onClick={() =>
          onChange([...rows, { key: '', action: 'set', value: '' }])
        }
      >
        添加请求头
      </button>
    </section>
  )
}
