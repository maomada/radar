import { Lock } from 'lucide-react'

export function OperatorManagedNotice({ helmValue }: { helmValue?: string }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-elevated/50 p-3 text-sm">
      <Lock className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
      <div className="min-w-0">
        <p className="font-medium text-theme-text-primary">由运维人员管理</p>
        <p className="mt-1 text-xs text-theme-text-secondary">
          如需更改这些设置，请联系运维人员更新
          {helmValue ? <> <code className="font-mono">{helmValue}</code> Helm 值。</> : ' Radar 的 Helm 值或启动配置。'}
          {' '}<a className="text-accent hover:underline" href="https://github.com/skyhook-io/radar/blob/main/docs/in-cluster.md#installation-settings" target="_blank" rel="noreferrer">配置指南</a>
        </p>
      </div>
    </div>
  )
}
