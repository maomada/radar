import { describe, expect, it } from 'vitest'
import { filterClusters, mcpClientConfig, safeExternalURL } from './model'
import type { PortalCluster } from './model'

const clusters: PortalCluster[] = [
  { id: 'prod-east', name: 'Production', description: 'Payments', environment: 'production', region: 'East', radarUrl: 'https://radar.example.com/teams/prod/', radarVersion: 'dev', runtimeMode: 'in-cluster', connectionState: 'connected', authMode: 'oidc', mcp: { mode: 'read-only', transport: 'streamable-http', readOnlyUrl: 'https://radar.example.com/teams/prod/mcp-readonly' }, registeredAt: '2026-01-01T00:00:00Z', lastSeen: '2026-01-01T00:00:00Z', status: 'online' },
  { id: 'dev-west', name: 'Development', description: '', environment: 'development', region: 'West', radarUrl: 'https://dev.example.com/', radarVersion: 'dev', runtimeMode: 'local', kubeContext: 'lab-admin', clusterName: 'sandbox', connectionState: 'disconnected', authMode: 'none', mcp: { mode: 'disabled', transport: 'streamable-http' }, registeredAt: '2026-01-01T00:00:00Z', lastSeen: '2026-01-01T00:00:00Z', status: 'stale' },
]

describe('portal directory', () => {
  it('combines metadata search with environment and heartbeat filters', () => {
    expect(filterClusters(clusters, '  PAYMENTS ', 'production', 'online')).toEqual([clusters[0]])
    expect(filterClusters(clusters, 'west', '', 'stale')).toEqual([clusters[1]])
    expect(filterClusters(clusters, '', 'production', 'stale')).toEqual([])
    expect(filterClusters([], '', '', '')).toEqual([])
    expect(filterClusters(clusters, 'lab-admin', '', '')).toEqual([clusters[1]])
    expect(filterClusters(clusters, 'sandbox', '', '')).toEqual([clusters[1]])
  })

  it('keeps the exact registered readonly endpoint including its path prefix', () => {
    const url = clusters[0].mcp.readOnlyUrl!
    expect(JSON.parse(mcpClientConfig('prod-east', url))).toEqual({ mcpServers: { 'radar-prod-east': { type: 'http', url } } })
  })

  it('refuses executable links and embedded credentials', () => {
    for (const url of ['javascript:alert(1)', '//evil.example', 'https://user:pass@example.com', 'https://example.com/?token=secret', 'https://example.com/#token', 'https://example.com/\\evil', 'https://example.com/\n']) {
      expect(safeExternalURL(url)).toBeUndefined()
      expect(() => mcpClientConfig('prod', url)).toThrow()
    }
    expect(safeExternalURL('https://radar.example.com/prefix/')).toBe('https://radar.example.com/prefix/')
  })
})
