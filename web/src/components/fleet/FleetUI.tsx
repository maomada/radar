import type { ButtonHTMLAttributes, ReactNode } from 'react'
import { Badge } from '@skyhook-io/k8s-ui'

export const inputClass = 'w-full rounded-md border border-theme-border bg-theme-elevated px-3 py-2 text-sm text-theme-text-primary'

export function Button({ variant = 'default', size, className = '', type = 'button', ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'default' | 'outline' | 'ghost'; size?: 'sm' }) {
  const style = variant === 'default' ? 'btn-brand' : variant === 'outline' ? 'border border-theme-border bg-theme-surface hover:bg-theme-hover' : 'hover:bg-theme-hover'
  return <button type={type} className={`inline-flex items-center justify-center gap-2 rounded-lg text-sm disabled:opacity-50 disabled:cursor-not-allowed focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent ${size === 'sm' ? 'px-3 py-1.5' : 'px-3 py-2'} ${style} ${className}`} {...props} />
}

export function Panel({ title, children }: { title: string; children: ReactNode }) {
  return <section className="rounded-xl border border-theme-border bg-theme-surface p-5 space-y-4"><h2 className="text-base font-medium text-theme-text-primary">{title}</h2>{children}</section>
}

export function ErrorNotice({ error }: { error: unknown }) {
  if (!error) return null
  return <p role="alert" className="text-sm text-warning-text">{error instanceof Error ? error.message : 'The request could not be completed.'}</p>
}

export function State({ value }: { value: string }) {
  const state = value.toLowerCase()
  const severity = ['critical', 'error', 'failed'].includes(state) ? 'error' : ['conflict', 'identity_conflict', 'unavailable', 'denied', 'outcome_unknown', 'observed_issues', 'warning'].includes(state) ? 'warning' : 'neutral'
  return <Badge severity={severity}>{value.replaceAll('_', ' ')}</Badge>
}
