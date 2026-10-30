import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { formatRadarVersion, radarUpgradeDetail, radarUpgradeHeadline, RadarUpgradeNote } from './RadarUpgradeNote'
import { getRadarUpgradeRequirement } from '../../types/fetch-error'

describe('radarUpgradeHeadline', () => {
  it('says the cluster is behind, not the UI', () => {
    expect(radarUpgradeHeadline('Capacity')).toBe('此集群需要更新版本的 Radar 才能使用容量')
  })
})

describe('radarUpgradeDetail', () => {
  it('quotes the minimum and a real current version', () => {
    expect(radarUpgradeDetail({ feature: 'Policy results', minimumVersion: 'v1.10.0', currentVersion: '1.7.2' }))
      .toBe('Radar v1.10 起提供此功能。此集群当前运行 v1.7.2。')
  })

  it('leaves out a current version that is not a version', () => {
    expect(radarUpgradeDetail({ feature: 'Policy results', minimumVersion: 'v1.10.0', currentVersion: 'dev' }))
      .toBe('Radar v1.10 起提供此功能。')
    expect(radarUpgradeDetail({ feature: 'Policy results', minimumVersion: 'v1.10.0' }))
      .toBe('Radar v1.10 起提供此功能。')
  })
})

describe('radarUpgradeDetail without a known first release', () => {
  const unknownSince = { feature: 'Drift alerts', currentVersion: 'v1.7.2' }

  it('asks for the latest release when it is newer than the running one', () => {
    expect(radarUpgradeDetail({ ...unknownSince, latestVersion: '1.16.0' }))
      .toBe('最新版本 Radar（v1.16）已提供此功能。此集群当前运行 v1.7.2。')
  })

  it('only names the running version when no target release is known', () => {
    expect(radarUpgradeDetail(unknownSince)).toBe('此集群当前运行 v1.7.2。')
    expect(radarUpgradeDetail({ ...unknownSince, currentVersion: 'v1.16.0', latestVersion: 'v1.16.0' }))
      .toBe('此集群当前运行 v1.16。')
  })

  it('reads the same way inline', () => {
    const html = renderToString(<RadarUpgradeNote requirement={{ ...unknownSince, latestVersion: 'v1.16.0' }} />)
    expect(html).toContain('漂移告警')
    expect(html).toContain('需要最新版本的 Radar（v1.16）。此集群当前运行 v1.7.2。')
  })
})

describe('formatRadarVersion', () => {
  it('drops a zero patch and adds the v', () => {
    expect(formatRadarVersion('v1.14.0')).toBe('v1.14')
    expect(formatRadarVersion('1.7.2')).toBe('v1.7.2')
    expect(formatRadarVersion('v1.7.2-dirty')).toBe('v1.7.2-dirty')
  })
})

describe('getRadarUpgradeRequirement', () => {
  it('reads the requirement off any error that carries one', () => {
    const error = Object.assign(new Error('x'), { radarUpgrade: { feature: 'Drain plans', minimumVersion: 'v1.14.0' } })
    expect(getRadarUpgradeRequirement(error)).toEqual({ feature: 'Drain plans', minimumVersion: 'v1.14.0' })
  })

  it('ignores plain errors and malformed requirements', () => {
    expect(getRadarUpgradeRequirement(new Error('HTTP 404 (Not Found)'))).toBeNull()
    expect(getRadarUpgradeRequirement({ radarUpgrade: { minimumVersion: 'v1.9.0' } })).toBeNull()
    expect(getRadarUpgradeRequirement({ radarUpgrade: { feature: 'x', minimumVersion: 9 } })).toBeNull()
    expect(getRadarUpgradeRequirement(null)).toBeNull()
  })
})
