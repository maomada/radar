import { useEffect, useState } from 'react'
import { ExternalLink } from 'lucide-react'
import { Collapse, CollapseChevron, useDisclosure } from '@skyhook-io/k8s-ui/components/ui/Collapse'
import { ConfigToggle, SubHeading } from './controls'
import { isRecording, useSetUsageData, useUsageData, type UsageDataStatus } from '../../api/usage-data'

const DOCS_URL = 'https://radarhq.io/docs/configuration/usage-stats'

export function PrivacySection({ active }: { active: boolean }) {
  const { data: status, isLoading, error, refetch } = useUsageData(active, { fresh: true })
  const setUsageData = useSetUsageData()

  // The pane stays mounted while hidden, so refetch each time it is revealed.
  useEffect(() => {
    if (active) void refetch()
  }, [active, refetch])

  if (isLoading) {
    return <p className="text-sm text-theme-text-tertiary">加载中…</p>
  }
  if (error || !status) {
    return <p className="text-sm text-theme-text-secondary">无法加载隐私设置。</p>
  }

  const recording = isRecording(status)

  return (
    <div className="space-y-6">
      <p className="text-sm text-theme-text-secondary">
        {status.preview.mode === 'in-cluster' || status.preview.mode === 'cloud'
          ? 'Radar reads this cluster from inside it and keeps what it reads there.'
          : 'Radar reads your cluster from this machine and keeps what it reads here.'}
      </p>

      <section className="space-y-3">
        <SubHeading>使用情况统计</SubHeading>
        <ConfigToggle
          label="发送匿名使用情况统计"
          description={<>帮助我们了解功能使用情况，以决定后续开发方向。<br />数据直接发送给我们，不使用第三方跟踪器。</>}
          value={recording}
          disabled={!status.canChange || setUsageData.isPending}
          onChange={(v) => setUsageData.mutate(v)}
        />
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5 text-xs">
          <dt className="text-theme-text-tertiary">发送内容</dt>
          <dd className="text-theme-text-secondary">
            你打开的视图、使用的操作和 MCP 工具，以及每个集群的版本、平台、规模范围和已知集成。
          </dd>
          <dt className="text-theme-text-tertiary">不会发送</dt>
          <dd className="text-theme-text-secondary">
            名称、内容、日志、标识符、时间戳或集群中的资源。
          </dd>
        </dl>
        <ManagedReason status={status} />
        <SharedNote status={status} />
        <ReportPreview status={status} />
      </section>

    </div>
  )
}

function ManagedReason({ status }: { status: UsageDataStatus }) {
  let text: string | null = null
  switch (status.source) {
    case 'env':
      text = status.shared && status.preview.mode === 'in-cluster'
        ? '通过 Helm 值 usageReporting.enabled 为所有用户设置。'
        : `由 RADAR_USAGE_REPORTING=${status.state} 设置。`
      break
    case 'deployment':
      text = 'Radar Cloud 管理此安装的使用情况数据。'
      break
  }
  if (!text && status.developmentBuild && isRecording(status)) {
    text = '开发版本：报告仅写入日志，不会发送。'
  }
  return text ? <p className="text-xs text-theme-text-tertiary">{text}</p> : null
}

// A shared Radar is decided by its configuration; say where, while nothing set it.
function SharedNote({ status }: { status: UsageDataStatus }) {
  if (!status.shared || status.source !== 'default') return null
  return (
    <p className="text-xs text-theme-text-tertiary">
      适用于所有使用此 Radar 的用户。可通过以下方式设置：{' '}
      {status.preview.mode === 'in-cluster'
        ? <>the Helm value <code className="inline-code">usageReporting.enabled</code></>
        : <code className="inline-code">RADAR_USAGE_REPORTING=on</code>}.
    </p>
  )
}

function ReportPreview({ status }: { status: UsageDataStatus }) {
  const recording = isRecording(status)
  const [open, setOpen] = useState(false)
  const disclosure = useDisclosure(open)
  // Development builds write the report to the log instead of sending it.
  const logsOnly = status.developmentBuild
  const next = status.nextReportAt ? new Date(status.nextReportAt) : null
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-3">
        <button
          type="button"
          onClick={() => setOpen(o => !o)}
          {...disclosure.buttonProps}
          className="flex items-center gap-1 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary"
        >
          <CollapseChevron open={open} />
          {recording ? '查看下一份报告' : '查看示例报告'}
          {recording && next && (
            <span className="font-normal text-theme-text-tertiary">
              {' '}· {logsOnly ? '写入日志' : '发送'} {next.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}
            </span>
          )}
        </button>
        <a
          href={DOCS_URL}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:underline"
        >
          工作原理 <ExternalLink className="w-3 h-3" aria-hidden />
        </a>
      </div>
      <Collapse open={open} id={disclosure.panelId}>
        <pre className="max-h-80 overflow-auto rounded-md border border-theme-border bg-theme-elevated px-3 py-2 text-[11px] leading-relaxed font-mono text-theme-text-primary">
          {JSON.stringify(status.preview, null, 2)}
        </pre>
      </Collapse>
      {recording && status.canChange && (
        <p className="text-xs text-theme-text-tertiary">
          待处理报告位于 <code className="inline-code">~/.radar/usage-report.json</code>。关闭此选项会删除该文件。
        </p>
      )}
    </div>
  )
}
