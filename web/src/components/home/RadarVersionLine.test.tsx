import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { VersionInfo } from '../../api/client'
import { RadarVersionLine } from './RadarVersionLine'

const version: VersionInfo = {
  currentVersion: '1.2.3',
  latestVersion: '1.3.0',
  updateAvailable: true,
  installMethod: 'direct',
  releaseUrl: 'https://github.com/skyhook-io/radar/releases/tag/v1.3.0',
}

describe('RadarVersionLine', () => {
  it('shows the running version without an upgrade affordance when up to date', () => {
    const html = renderToString(
      <RadarVersionLine version={{ ...version, latestVersion: '1.2.3', updateAvailable: false }} />,
    )
    expect(html).toContain('Radar')
    expect(html).toContain('v1.2.3')
    expect(html).not.toContain('可用版本')
  })

  it('shows patch upgrades with the quiet treatment', () => {
    const html = renderToString(
      <RadarVersionLine version={{ ...version, latestVersion: '1.2.4' }} />,
    )
    expect(html).toContain('v1.2.3')
    expect(html).toContain('v1.2.4')
    expect(html).toContain('可用版本')
    expect(html).toContain('text-accent-text hover:text-accent')
  })

  it('makes minor upgrades more prominent', () => {
    const html = renderToString(<RadarVersionLine version={version} />)
    expect(html).toContain('font-medium text-accent hover:text-accent-light')
    expect(html).not.toContain('次版本')
  })

  it('uses warning emphasis and explains when an installation is three minor releases behind', () => {
    const html = renderToString(
      <RadarVersionLine version={{ ...version, currentVersion: '1.0.9', latestVersion: '1.3.0' }} />,
    )
    expect(html).toContain('font-medium text-warning-text hover:opacity-80')
    expect(html).toContain('此安装落后 3 个次版本。')
  })

  it('uses warning emphasis for a major upgrade', () => {
    const html = renderToString(
      <RadarVersionLine version={{ ...version, currentVersion: '0.12.0', latestVersion: '1.3.0' }} />,
    )
    expect(html).toContain('font-medium text-warning-text hover:opacity-80')
    expect(html).toContain('有可用的 Radar 主版本升级。')
  })

  it('does not claim manager discovery has failed while it is loading', () => {
    const html = renderToString(<RadarVersionLine version={version} managerLoading />)
    expect(html).toContain('v1.3.0')
    expect(html).toContain('可用版本')
    expect(html).toContain('lucide-circle-arrow-up')
    expect(html).toContain('正在检查此安装的管理方式。')
    expect(html).not.toContain('无法确认安装管理器')
    expect(html).not.toContain('<a')
    expect(html).not.toContain('<button')
    expect(html).toContain('class="sr-only"')
    expect(html).not.toContain('aria-label=')
  })

  it('opens actionable upgrade instructions when the installation manager is unknown', () => {
    const html = renderToString(<RadarVersionLine version={version} />)
    expect(html).toContain('https://radarhq.io/docs/configuration/in-cluster')
    expect(html).not.toContain('#upgrading')
    expect(html).toContain('无法确认安装管理器。请打开集群内升级说明。')
    expect(html).not.toContain(version.releaseUrl)
    expect(html).toContain('font-medium text-accent hover:text-accent-light')
  })

  it('keeps the visible upgrade label in the accessible name', () => {
    const html = renderToString(<RadarVersionLine version={version} />)
    expect(html).toContain('aria-label="可用版本 v1.3.0 —')
  })

  it('deep-links exact Helm ownership when the host supports it', () => {
    const html = renderToString(
      <RadarVersionLine
        version={version}
        manager={{ ownership: 'helm', namespace: 'radar-system', release: 'radar' }}
        onNavigateToHelmRelease={() => {}}
      />,
    )
    expect(html).toContain('由 Helm 发布 radar-system/radar 管理。打开该发布进行升级。')
  })

  it('describes the docs fallback when a Helm navigation callback is unavailable', () => {
    const html = renderToString(
      <RadarVersionLine
        version={version}
        manager={{ ownership: 'helm', namespace: 'radar-system', release: 'radar' }}
      />,
    )
    expect(html).toContain('https://radarhq.io/docs/configuration/in-cluster')
    expect(html).toContain('由 Helm 发布 radar-system/radar 管理。打开集群内升级说明。')
    expect(html).not.toContain('打开该发布进行升级')
  })

  it('deep-links verified GitOps ownership', () => {
    const controllerRef = { group: 'kustomize.toolkit.fluxcd.io', kind: 'Kustomization', namespace: 'flux-system', name: 'radar' }
    const verified = renderToString(
      <RadarVersionLine
        version={version}
        manager={{ ownership: 'gitops', controller: 'Kustomization flux-system/radar', controllerRef }}
        onNavigateToGitOps={() => {}}
      />,
    )
    expect(verified).toContain('由 Kustomization flux-system/radar 管理。打开它并通过 GitOps 升级。')

    const suspected = renderToString(
      <RadarVersionLine
        version={version}
        manager={{ ownership: 'gitops', controller: 'Kustomization flux-system/radar' }}
        onNavigateToGitOps={() => {}}
      />,
    )
    expect(suspected).toContain('此安装似乎由 GitOps（Kustomization flux-system/radar）管理。')
    expect(suspected).toContain('打开升级说明')
    expect(suspected).not.toContain('由 Kustomization')
  })

  it('describes the docs fallback when a GitOps navigation callback is unavailable', () => {
    const controllerRef = { group: 'kustomize.toolkit.fluxcd.io', kind: 'Kustomization', namespace: 'flux-system', name: 'radar' }
    const html = renderToString(
      <RadarVersionLine
        version={version}
        manager={{ ownership: 'gitops', controller: 'Kustomization flux-system/radar', controllerRef }}
      />,
    )
    expect(html).toContain('https://radarhq.io/docs/configuration/in-cluster')
    expect(html).toContain('由 Kustomization flux-system/radar 管理。打开集群内升级说明并通过 GitOps 应用更改。')
    expect(html).not.toContain('打开它并通过 GitOps 升级')
  })

  it('offers What\'s new only when the host can open it', () => {
    const upToDate = { ...version, latestVersion: '1.2.3', updateAvailable: false }
    expect(renderToString(<RadarVersionLine version={upToDate} />)).not.toContain('What&#x27;s new')
    expect(renderToString(<RadarVersionLine version={upToDate} onShowWhatsNew={() => {}} />)).toContain('更新内容')
    expect(renderToString(<RadarVersionLine version={version} onShowWhatsNew={() => {}} />)).toContain('更新内容')
  })

  it('hides upgrade guidance when the host handles upgrades elsewhere', () => {
    const html = renderToString(<RadarVersionLine version={version} showUpgrade={false} onShowWhatsNew={() => {}} />)
    expect(html).toContain('v1.2.3')
    expect(html).not.toContain('可用版本')
    expect(html).toContain('更新内容')
  })
})
