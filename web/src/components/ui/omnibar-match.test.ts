import { describe, expect, it } from 'vitest'
import { contentMatchLabel } from './omnibar-match'

const site = (s: string) => ({ token: 't', site: s, score: 12 })

describe('contentMatchLabel', () => {
  it('names a match inside Trivy findings', () => {
    expect(contentMatchLabel([site('content:report.vulnerabilities[3]')])).toBe('in findings')
  })
  it('keeps "in spec" for other content', () => {
    expect(contentMatchLabel([site('content:spec.template.spec.containers[0].env[1].value')])).toBe('in spec')
    expect(contentMatchLabel([site('content:report.checks[0]'), site('content:spec.x')])).toBe('in spec')
  })
  it('is empty when any token matched identity', () => {
    expect(contentMatchLabel([site('content:report.vulnerabilities[0]'), site('name')])).toBe('')
    expect(contentMatchLabel([])).toBe('')
    expect(contentMatchLabel(undefined)).toBe('')
  })
})
