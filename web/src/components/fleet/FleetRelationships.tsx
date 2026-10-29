import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fleetQuery, type FleetCluster, type FleetRelationships as RelationshipsResult, type FleetTopology } from '../../api/fleet'
import { Button, ErrorNotice, Panel, State } from './FleetUI'
import { Limitations, Provenance } from './FleetEvidence'

export function FleetRelationships({ fleetId, clusters, queryRoot }: { fleetId: string; clusters: FleetCluster[]; queryRoot: unknown[] }) {
  const [view, setView] = useState<'relationships' | 'topology'>('relationships')
  const result = useQuery({
    queryKey: [...queryRoot, view, fleetId],
    queryFn: ({ signal }) => fleetQuery<RelationshipsResult | FleetTopology>(view === 'relationships' ? 'get_fleet_relationships' : 'get_fleet_topology', fleetId ? { fleetId } : {}, signal),
    retry: false, gcTime: 0,
  })
  const data = result.error || result.isFetching ? undefined : result.data
  const clusterName = (id?: string) => id ? `${clusters.find(cluster => cluster.clusterId === id)?.name ?? id} (${id})` : 'Unresolved target'
  const nodeLabel = (id: string) => {
    const node = data && 'nodes' in data ? data.nodes.find(item => item.id === id) : undefined
    return node ? `${node.label} · ${clusterName(node.clusterId)}` : id
  }
  return <Panel title="Evidence-backed relationships">
    <p className="text-sm text-theme-text-secondary">Karmada membership and distribution declarations are not proof of physical identity, controller ownership or successful delivery. Ordinary Kubernetes clusters do not need Karmada.</p>
    <div className="flex flex-wrap gap-2"><Button variant={view === 'relationships' ? 'default' : 'outline'} onClick={() => setView('relationships')}>Relationships</Button><Button variant={view === 'topology' ? 'default' : 'outline'} onClick={() => setView('topology')}>Topology</Button><Button variant="ghost" disabled={result.isFetching} onClick={() => void result.refetch()}>Refresh evidence</Button></div>
    <ErrorNotice error={result.error} />
    {result.isFetching && <p role="status" className="text-sm">Reading bounded relationship evidence…</p>}
    {data && <>
      <Limitations values={data.limitations} truncated={data.truncated} />
      <details className="text-xs text-theme-text-secondary"><summary className="cursor-pointer">Collection coverage</summary><ul className="mt-2 space-y-2">{data.coverage.map((row, index) => <li key={index}>{clusterName(row.clusterId)} · {row.resource} <State value={row.state} /></li>)}</ul></details>
      {'relationships' in data && <div className="space-y-4">{data.relationships.map(relationship => <article key={relationship.id} className="rounded-lg border border-theme-border p-4 space-y-3">
        <p className="text-sm font-medium">{relationship.type.replaceAll('_', ' ')} <State value={relationship.state} /></p>
        <p className="text-xs break-all">{clusterName(relationship.sourceClusterId)} → {clusterName(relationship.targetClusterId)}</p>
        {relationship.memberName && <p className="text-xs text-theme-text-secondary">Declared member: {relationship.memberName}</p>}
        {!!relationship.targetCandidates?.length && <p className="text-xs text-warning-text">Ambiguous candidates: {relationship.targetCandidates.map(clusterName).join('; ')}. Kept separate.</p>}
        {relationship.sourceResource && <details className="text-xs"><summary className="cursor-pointer">Observed source resource</summary><Provenance target={relationship.sourceResource} /></details>}
        {relationship.targetResource && <details className="text-xs"><summary className="cursor-pointer">Observed target resource</summary><Provenance target={relationship.targetResource} /></details>}
        <details className="text-xs"><summary className="cursor-pointer">Recorded evidence ({relationship.evidence.length})</summary><div className="mt-3 space-y-4">{relationship.evidence.map((evidence, index) => <Provenance key={index} target={evidence} />)}</div></details>
      </article>)}{data.relationships.length === 0 && <p className="text-sm text-theme-text-secondary">No relationship established in the collected scope. Unsupported or denied APIs are not proof that no relationship exists.</p>}</div>}
      {'nodes' in data && <>
        <p className="text-sm text-theme-text-secondary">{data.nodes.length} returned nodes · {data.edges.length} evidence-backed edges</p>
        <div className="overflow-x-auto"><table className="w-full text-sm text-left"><caption className="text-left text-xs text-theme-text-tertiary mb-2">Topology edges; unresolved destinations remain explicit nodes.</caption><thead><tr><th className="py-2">Source</th><th>Relationship</th><th>Target</th><th>Observation</th></tr></thead><tbody>{data.edges.map(edge => <tr key={edge.id} className="border-t border-theme-border"><td className="py-3 break-all">{nodeLabel(edge.source)}</td><td>{edge.type}</td><td className="break-all">{nodeLabel(edge.target)}</td><td><State value={edge.state} /><details className="text-xs mt-2"><summary className="cursor-pointer">Evidence</summary><div className="space-y-3">{edge.evidence.map((evidence, index) => <Provenance key={index} target={evidence} />)}</div></details></td></tr>)}</tbody></table></div>
        <details className="text-xs"><summary className="cursor-pointer">Node identities</summary><ul className="mt-2 space-y-2">{data.nodes.map(node => <li key={node.id} className="break-all">{node.label} · {node.type} · {clusterName(node.clusterId)}<div className="font-mono text-theme-text-tertiary">{node.id}{node.uid && ` · UID ${node.uid}`}</div></li>)}</ul></details>
      </>}
    </>}
  </Panel>
}
