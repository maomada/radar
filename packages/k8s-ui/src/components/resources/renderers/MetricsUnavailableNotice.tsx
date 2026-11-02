import { Info } from 'lucide-react'
import { Tooltip } from '../../ui/Tooltip'

interface MetricsUnavailableNoticeProps {
  noUsageYet?: boolean
  rawError?: string
  diagnosis?: string
}

export function MetricsUnavailableNotice({ rawError, diagnosis, noUsageYet }: MetricsUnavailableNoticeProps) {
  return (
    <div className="card-inner-lg text-xs text-theme-text-tertiary">
      <div className="flex items-center gap-1.5">
        <span className="min-w-0 leading-5">
          {noUsageYet ? '暂无用量：此 Pod 尚未运行' : '指标不可用。Radar 无法读取 metrics.k8s.io。'}
        </span>
        {rawError && (
          <Tooltip
            content={(
              <span className="space-y-1">
                <span className="block">
                  此面板使用 Kubernetes metrics.k8s.io 获取实时 CPU 和内存；Prometheus 数据不会填充此处。
                </span>
                <span className="block">
                  {diagnosis ||
                    "请检查 metrics-server 是否已安装且运行正常，并确认其 metrics.k8s.io APIService 可用。"}
                </span>
                <span className="block">
                  原始错误：<span className="font-mono break-words">{rawError}</span>
                </span>
              </span>
            )}
            delay={150}
            position="left"
          >
            <button
              type="button"
              className="inline-flex shrink-0 cursor-help items-center gap-1 text-xs font-medium leading-5 text-theme-text-tertiary hover:text-theme-text-secondary"
              aria-label="指标错误详情"
            >
              <Info className="h-3.5 w-3.5" />
              <span>详情</span>
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  )
}
