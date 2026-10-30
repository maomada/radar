export interface PortalCluster {
  id: string
  name: string
  description: string
  environment: string
  region: string
  radarUrl: string
  radarVersion: string
  runtimeMode: 'local' | 'in-cluster'
  kubeContext?: string
  clusterName?: string
  connectionState: 'connected' | 'connecting' | 'disconnected'
  authMode: 'none' | 'proxy' | 'oidc'
  mcp: {
    mode: 'full' | 'read-only' | 'disabled'
    transport: 'streamable-http'
    url?: string
    readOnlyUrl?: string
  }
  registeredAt: string
  lastSeen: string
  status: 'online' | 'stale'
}

export interface PortalDirectory {
  clusters: PortalCluster[]
  serverTime: string
  staleAfterSeconds: number
}

export function safeExternalURL(value: string): string | undefined {
  try {
    const url = new URL(value)
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash || /[\s\\]/.test(value)) return undefined
    return url.href
  } catch {
    return undefined
  }
}

export function filterClusters(clusters: PortalCluster[], query: string, environment: string, status: string): PortalCluster[] {
  const needle = query.trim().toLocaleLowerCase()
  return clusters.filter(cluster =>
    (!environment || cluster.environment === environment) &&
    (!status || cluster.status === status) &&
      [cluster.name, cluster.id, cluster.description, cluster.environment, cluster.region, cluster.radarUrl, cluster.kubeContext || '', cluster.clusterName || '']
      .some(value => value.toLocaleLowerCase().includes(needle)),
  )
}

export function mcpClientConfig(id: string, url: string): string {
  if (!safeExternalURL(url)) throw new Error('MCP 地址无效')
  return JSON.stringify({ mcpServers: { [`radar-${id}`]: { type: 'http', url } } }, null, 2)
}
