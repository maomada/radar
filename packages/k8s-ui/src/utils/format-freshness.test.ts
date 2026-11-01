import { describe, it, expect } from 'vitest'
import { formatUpdatedAgo, formatLastUpdatedBucket, msToNextBucket } from './format'

describe('formatUpdatedAgo', () => {
  it('collapses the first minute to a just-now label', () => {
    expect(formatUpdatedAgo(0)).toBe('刚刚')
    expect(formatUpdatedAgo(59_000)).toBe('刚刚')
  })
  it('renders minutes and hours in Chinese', () => {
    expect(formatUpdatedAgo(60_000)).toBe('1分钟前')
    expect(formatUpdatedAgo(3_600_000)).toBe('1小时前')
  })
  it('collapses anything past a day to an inexact label', () => {
    expect(formatUpdatedAgo(25 * 3_600_000)).toBe('超过一天')
  })
})

describe('formatLastUpdatedBucket', () => {
  it('buckets by the coarsest unit', () => {
    expect(formatLastUpdatedBucket(0)).toBe('just now')
    expect(formatLastUpdatedBucket(60_000)).toBe('1m')
    expect(formatLastUpdatedBucket(3_600_000)).toBe('1h')
    expect(formatLastUpdatedBucket(86_400_000)).toBe('1d')
  })
})

describe('msToNextBucket', () => {
  it('schedules the next re-render exactly on the bucket boundary', () => {
    expect(msToNextBucket(0)).toBe(60_000)
    expect(msToNextBucket(30_000)).toBe(30_000)
    // 90s elapsed → 30s until the "2m" bucket flips.
    expect(msToNextBucket(90_000)).toBe(30_000)
  })
})
