import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import {
  LocalConnectionSettings,
  type IntegrationProfile,
  type IntegrationProfiles
} from './LocalConnectionSettings'
import { PreviousIntegrationSettingsNotice } from './LocalConfigurationDetails'

const profile: IntegrationProfile = {
  target: {
    binding: 'opaque',
    context: 'development',
    source: '/configs/team',
    inFileName: 'dev',
    fingerprint: 'target',
    clientGeneration: 1,
    operationGeneration: 1,
    identity: {
      server: 'https://kubernetes',
      user: 'developer',
      trust: 'trust',
      insecureTls: false
    }
  },
  revision: 'revision',
  state: 'auto',
  mode: 'auto',
  url: '',
  headerKeys: [],
  envHeaderKeys: [],
  headersManaged: false,
  secretSet: false,
  insecureTls: false,
  clusterId: ''
}
const render = (
  changes: Partial<IntegrationProfile>,
  kind: keyof IntegrationProfiles = 'metrics'
) => {
  const profiles: IntegrationProfiles = {
    metrics: profile,
    argocd: profile,
    cost: profile,
    [kind]: { ...profile, ...changes }
  }
  return renderToStaticMarkup(
    <LocalConnectionSettings
      kind={kind}
      profiles={profiles}
      onChange={vi.fn()}
      onDirtyChange={vi.fn()}
    />
  )
}
const savedProfile: Partial<IntegrationProfile> = {
  state: 'saved',
  url: 'https://metrics',
  headerKeys: ['Authorization', 'X-Scope-OrgID'],
  target: { ...profile.target, binding: 'development' }
}

describe('Local saved connections', () => {
  it('only advertises previous settings for unconfigured integrations', () => {
    const legacy = { url: 'https://previous', headerKeys: [], envHeaderKeys: [], secretSet: false, insecureTls: false, mode: 'auto', clusterId: '', revision: 'previous', omitted: [], noticeDismissed: false }
    const profiles: IntegrationProfiles = {
      metrics: { ...profile, legacy },
      argocd: { ...profile, state: 'saved', legacy },
      cost: { ...profile, state: 'launch', legacy },
    }
    const notice = () => renderToStaticMarkup(<PreviousIntegrationSettingsNotice profiles={profiles} onNavigate={vi.fn()} onDismissed={vi.fn()} />)
    const html = notice()
    expect(html).toContain('集成设置现按集群保存')
    expect(html).toContain('查看 Metrics')
    expect(html).not.toContain('Review Argo CD')
    expect(html).not.toContain('Review Cost')
    profiles.metrics.legacy = { ...legacy, noticeDismissed: true }
    expect(notice()).toBe('')
    delete profiles.metrics.legacy
    expect(notice()).toBe('')
  })
  it.each(['metrics', 'argocd', 'cost'] as const)(
    'does not show a routine reload action for %s',
    (kind) => {
      for (const state of [
        'auto',
        'saved',
        'launch',
        'target_changed'
      ] as const) {
        expect(render({ state }, kind)).not.toContain('重新加载')
      }
    }
  )
  it('keeps first setup a form, with no mandatory name or assignment step', () => {
    const html = render({})
    expect(html).toContain('保存更改')
    expect(html).toContain('添加请求头')
    expect(html).not.toContain('复制设置自')
    expect(html).toContain('集群设置身份')
  })
  it('names common compatible backends without claiming an exhaustive list', () => {
    const html = render({ headerKeys: ['Authorization', 'X-Scope-OrgID'] })
    expect(html).toContain(
      '例如 Prometheus、VictoriaMetrics、Thanos 或 Grafana Mimir'
    )
    expect(html).toContain('身份验证请求头')
    expect(html).toContain('Authorization的值')
    expect(html).toContain('X-Scope-OrgID的值')
    expect(html).toContain('已保存的值')
    expect(html).not.toContain('修改请求头')
    expect(html).not.toContain('<select')
  })
  it('offers previous settings as a copy source and never activates them by default', () => {
    const html = render({
      legacy: {
        url: 'https://legacy',
        headerKeys: ['Authorization'],
        envHeaderKeys: [],
        insecureTls: false,
        mode: 'connection',
        clusterId: '',
        secretSet: true,
        revision: 'legacy',
        omitted: [],
        noticeDismissed: true
      }
    })
    expect(html).toContain('复制设置自…')
    expect(html).not.toContain('https://legacy')
  })
  it('keeps launch overrides read-only with a way to change their source', () => {
    const html = render({
      state: 'launch',
      url: 'https://temporary',
      headerKeys: ['Authorization']
    })
    expect(html).toContain('仅本次启动生效')
    expect(html).toContain(
      '若要编辑已保存的设置，请在不使用这些启动标志或环境配置的情况下重新启动。'
    )
    expect(html).not.toContain('保存更改')
  })
  it('requires review for changed cluster identity', () => {
    const html = render({
      state: 'target_changed',
      url: 'https://saved',
      error: 'Review the changed cluster connection'
    })
    expect(html).toContain('查看更改')
    expect(html).not.toContain('保存更改')
  })
  it.each(['argocd', 'cost'] as const)(
    'lets paused discovery credentials be replaced without accepting them for %s',
    (kind) => {
      const html = render(
        { state: 'target_changed', url: '', secretSet: true, error: 'The cluster behind this context changed' },
        kind
      )
      expect(html).toContain('查看更改')
      expect(html).toContain('使用其他连接')
    }
  )
  it.each(['metrics', 'argocd', 'cost'] as const)(
    'offers contextual recovery rather than overwriting a malformed file for %s',
    (kind) => {
      const html = render(
        { state: 'error', error: 'Repair clusters.json' },
        kind
      )
      expect(html).toContain('Repair clusters.json')
      expect(html).toContain('重新加载最新设置')
      expect(html).not.toContain('保存更改')
    }
  )
  it.each(['metrics', 'argocd', 'cost'] as const)(
    'edits only this cluster for %s',
    (kind) => {
      const html = render({ ...savedProfile, secretSet: true }, kind)
      expect(html).toContain('保存更改')
      expect(html).not.toContain('Copy settings from')
      expect(html).not.toContain('Reload')
    }
  )
  it('keeps file details and saved-entry cleanup out of integration tabs', () => {
    const html = render(savedProfile)
    expect(html).not.toContain('clusters.json')
  })
})
