import { useEffect, useRef, useState } from 'react'
import { BarChart3, Check } from 'lucide-react'
import { useMarkUsagePromptShown, useSetUsageData, type UsageDataStatus } from '../../api/usage-data'

export const USAGE_DATA_COPY = {
  title: '帮助改进Radar',
  description: '匿名统计，不使用第三方跟踪器。',
  readMore: '查看发送内容',
  send: '发送使用情况统计',
  decline: '暂不发送',
  close: '关闭',
  thanks: '谢谢。你可以随时在“设置”>“隐私”中更改。',
  declined: '不会发送任何内容。你可以随时在“设置”>“隐私”中更改。',
} as const

// One wording for every place KubeAtlas asks, so the question can't drift.
export function UsageDataBlurb({ onReadMore }: { onReadMore: () => void }) {
  return (
    <>
      <p className="text-sm font-medium text-theme-text-primary">{USAGE_DATA_COPY.title}</p>
      <p className="mt-0.5 text-xs text-theme-text-secondary leading-relaxed">
        {USAGE_DATA_COPY.description}{' '}
        <button
          type="button"
          onClick={onReadMore}
          className="whitespace-nowrap text-theme-text-primary underline underline-offset-2 hover:text-accent-text"
        >
          {USAGE_DATA_COPY.readMore}
        </button>
      </p>
    </>
  )
}

// Shown in place of the question once answered, so it doesn't vanish under
// the cursor.
export function UsageDataAnswered({ answer }: { answer: boolean }) {
  return (
    <>
      <Check className="w-3.5 h-3.5 shrink-0 text-accent" aria-hidden />
      {answer
        ? USAGE_DATA_COPY.thanks
        : USAGE_DATA_COPY.declined}
    </>
  )
}

// The usage-data question, asked inside What's New when the server says to:
// someone who closed it without answering is asked again only months later,
// and a "no" is never asked again. A shared KubeAtlas, or one set by its
// configuration, never asks.
export function UsageDataAsk({ usageData, onReadMore }: {
  usageData: UsageDataStatus | undefined
  onReadMore: () => void
}) {
  const setUsageData = useSetUsageData()
  const markUsagePromptShown = useMarkUsagePromptShown()
  const [answer, setAnswer] = useState<boolean | null>(null)
  // Latched: recording the showing stops the server offering the question,
  // and the block must not vanish while the dialog is open.
  const [offered, setOffered] = useState(!!usageData?.ask)
  const recorded = useRef(false)

  useEffect(() => {
    if (usageData?.ask) setOffered(true)
  }, [usageData?.ask])
  useEffect(() => {
    if (offered && !recorded.current) {
      recorded.current = true
      markUsagePromptShown()
    }
  }, [offered, markUsagePromptShown])

  const undecided = offered && usageData?.state === 'undecided' && usageData.canChange
  if (!undecided && answer === null) return null

  if (answer !== null) {
    return (
      <div className="flex items-center gap-2 px-6 py-3 border-t border-theme-border text-xs text-theme-text-secondary">
        <UsageDataAnswered answer={answer} />
      </div>
    )
  }

  const choose = (enabled: boolean) => setUsageData.mutate(enabled, { onSuccess: () => setAnswer(enabled) })

  return (
    <div className="flex flex-col sm:flex-row sm:items-center gap-3 px-6 py-3.5 border-t border-theme-border">
      <div className="flex gap-3 min-w-0 flex-1">
        <span className="flex items-center justify-center w-8 h-8 shrink-0 rounded-lg bg-accent-muted text-accent">
          <BarChart3 className="w-4 h-4" aria-hidden />
        </span>
        <div className="min-w-0">
          <UsageDataBlurb onReadMore={onReadMore} />
        </div>
      </div>
      <div className="flex items-center gap-2 shrink-0 pl-11 sm:pl-0">
        <button
          type="button"
          disabled={setUsageData.isPending}
          onClick={() => choose(true)}
          className="px-3 py-1.5 text-xs font-medium rounded-lg border border-theme-border bg-theme-surface text-theme-text-primary hover:bg-theme-hover disabled:opacity-50"
        >
          {USAGE_DATA_COPY.send}
        </button>
        <button
          type="button"
          disabled={setUsageData.isPending}
          onClick={() => choose(false)}
          className="px-3 py-1.5 text-xs font-medium rounded-lg text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated disabled:opacity-50"
        >
          {USAGE_DATA_COPY.decline}
        </button>
      </div>
    </div>
  )
}
