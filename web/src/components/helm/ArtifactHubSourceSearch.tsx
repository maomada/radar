import { useState } from 'react'
import { Badge } from '@skyhook-io/k8s-ui'
import { useArtifactHubSearch } from '../../api/client'
import type { ArtifactHubChart } from '../../types'

type PublicChartSource = { type: 'repository'; url: string } | { type: 'oci'; url: string; prefix: string }

export function artifactHubChartSource(chart: ArtifactHubChart, chartName: string): PublicChartSource | undefined {
  if (chart.name !== chartName) return
  try {
    const url = new URL(chart.repository.url)
    if (!url.hostname || url.username || url.password || url.search || url.hash) return
    const address = url.toString().replace(/\/+$/, '')
    if (url.protocol === 'http:' || url.protocol === 'https:') return { type: 'repository', url: address }
    if (url.protocol === 'oci:' && url.pathname.replace(/\/+$/, '').endsWith(`/${chartName}`)) {
      return { type: 'oci', url: address, prefix: address.slice(0, -(chartName.length + 1)) }
    }
  } catch {
    return
  }
}

interface ArtifactHubSourceSearchProps {
  chartName: string
  disabled: boolean
  isConfigured: (source: PublicChartSource) => boolean
  onAdd: (chart: ArtifactHubChart, source: PublicChartSource) => void
}

const PAGE_SIZE = 60

export function ArtifactHubSourceSearch({ chartName, disabled, isConfigured, onAdd }: ArtifactHubSourceSearchProps) {
  const [started, setStarted] = useState(false)
  const [offset, setOffset] = useState(0)
  const search = useArtifactHubSearch(chartName, { offset, limit: PAGE_SIZE }, started)
  const seen = new Set<string>()
  const matches = (search.data?.charts ?? []).flatMap(chart => {
    const source = artifactHubChartSource(chart, chartName)
    if (!source || seen.has(source.url)) return []
    seen.add(source.url)
    return [{ chart, source }]
  })

  return <section className="space-y-2">
    <p className="text-sm font-medium">Find a public chart source</p>
    <p className="text-xs text-theme-text-secondary">Search ArtifactHub for the chart name "{chartName}". Search results do not prove where this release was installed from.</p>
    {!started && <button type="button" onClick={() => setStarted(true)} className="btn-brand px-3 py-2 text-sm">Search ArtifactHub</button>}
    {started && <div aria-live="polite" className="space-y-2">
      {search.isFetching && <p className="text-sm text-theme-text-secondary">Searching ArtifactHub...</p>}
      {search.error && <div role="alert" className="space-y-1">
        <p className="text-sm text-theme-text-secondary">Could not search ArtifactHub. You can still add a source manually below.</p>
        <button type="button" onClick={() => void search.refetch()} disabled={search.isFetching} className="text-sm text-accent hover:underline disabled:opacity-50">Retry search</button>
      </div>}
      {!search.isFetching && !search.error && search.data && matches.length === 0 && <p className="text-sm text-theme-text-secondary">No exact chart-name matches on this page. Try another page or add a source manually below.</p>}
      {matches.map(({ chart, source }) => <div key={source.url} className="rounded-lg border border-theme-border px-3 py-2 space-y-1">
        <p className="text-sm font-medium">{chart.repository.name}</p>
        {(chart.repository.organizationName || chart.repository.userAlias) && <p className="text-xs text-theme-text-secondary">Publisher: {chart.repository.organizationName || chart.repository.userAlias}</p>}
        <p className="font-mono text-xs text-theme-text-secondary break-all">{source.url}</p>
        {chart.repository.official && <Badge tone="note" size="sm">ArtifactHub official repository</Badge>}
        {chart.repository.verifiedPublisher && <Badge tone="note" size="sm">ArtifactHub verified publisher</Badge>}
        {isConfigured(source)
          ? <p className="text-xs text-theme-text-secondary">Already configured. Choose it in the source list above.{source.type === 'repository' ? ' If it is missing, refresh the repository in Helm Catalog.' : ''}</p>
          : <button type="button" onClick={() => onAdd(chart, source)} disabled={disabled || search.isFetching} aria-label={`Add source: ${source.url}`} className="block text-sm text-accent hover:underline disabled:opacity-50">{source.type === 'oci' ? 'Register OCI source' : 'Add repository'}</button>}
      </div>)}
      {search.data && <div className="flex items-center gap-3 text-xs">
        <span className="text-theme-text-tertiary">Search page {offset / PAGE_SIZE + 1}. Only exact chart-name matches are shown.</span>
        {offset > 0 && <button type="button" onClick={() => setOffset(offset - PAGE_SIZE)} disabled={search.isFetching} className="text-accent hover:underline disabled:opacity-50">Previous results</button>}
        {search.data.charts.length === PAGE_SIZE && <button type="button" onClick={() => setOffset(offset + PAGE_SIZE)} disabled={search.isFetching} className="text-accent hover:underline disabled:opacity-50">Next results</button>}
      </div>}
      {matches.length > 0 && <p className="text-xs text-theme-text-tertiary">Adding a source does not change the release. Then select it for an upgrade, or verify and record it as the original source.</p>}
    </div>}
  </section>
}
