import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Panel, ErrorNotice, State, inputClass } from './FleetUI'
import { FleetEvidence, Limitations, Provenance } from './FleetEvidence'
import { FleetRelationships } from './FleetRelationships'
import { FleetOperations } from './FleetOperations'
import { ArrowLeft, Network, RefreshCw, Search } from 'lucide-react'
import { useAuthMe } from '../../api/client'
import { getApiBase, routePath } from '../../api/config'
import {
  coverageCount, fleetQuery, fleetRequest, FleetHTTPError, useFleetCapabilities,
  type FleetCapabilities, type FleetCandidate, type FleetCluster, type FleetInventory,
  type FleetOverview, type FleetRecord, type FleetResource, type FleetSearch,
} from '../../api/fleet'

export default function FleetPage() {
  const auth = useAuthMe()
  const capabilities = useFleetCapabilities(!!auth.data && !auth.error, JSON.stringify([auth.data?.username, auth.data?.groups]))
  const authRequired = auth.data?.authEnabled && !auth.data.username
  return <div className="min-h-screen bg-theme-base text-theme-text-primary">
    <header className="border-b border-theme-border bg-theme-surface px-6 py-4 flex flex-wrap items-center justify-between gap-4">
      <div className="flex items-center gap-3"><Network className="text-accent h-6 w-6" /><div><h1 className="text-lg font-medium">Fleet Manager</h1><p className="text-xs text-theme-text-tertiary">Explicit cluster targets · isolated credentials · evidence-backed decisions</p></div></div>
      <Link to="/" className="flex items-center gap-2 text-sm text-accent"><ArrowLeft className="h-4 w-4" />Single-cluster Radar</Link>
    </header>
    <main className="max-w-7xl mx-auto p-4 sm:p-6 space-y-5">
      <details className="text-sm text-theme-text-secondary"><summary className="cursor-pointer">Connect an agent to Fleet MCP</summary><p className="mt-2">Use this one endpoint for Fleet tools. Authentication is the same as this Radar instance; do not put credentials in the URL.</p><code className="block mt-2 select-all break-all">{window.location.origin}{routePath('/mcp-fleet')}</code></details>
      {authRequired ? <Panel title="Authentication required"><p className="text-sm text-theme-text-secondary">Sign in with your Radar identity. Portal credentials do not grant Kubernetes access.</p>{auth.data?.authMode === 'oidc' ? <a className="text-accent" href={routePath('/auth/login')}>Sign in</a> : <p>Access this instance through its authentication proxy.</p>}</Panel>
        : auth.error ? <Panel title="Authentication unavailable"><ErrorNotice error={auth.error} /></Panel>
        : auth.isPending || capabilities.isPending ? <p role="status">Loading Fleet configuration…</p>
        : capabilities.error ? <Panel title="Fleet unavailable"><ErrorNotice error={capabilities.error} />{capabilities.error instanceof FleetHTTPError && capabilities.error.status === 404 && <p className="text-sm text-theme-text-secondary">Start Radar with a Fleet configuration to enable this workspace. Existing single-cluster behavior is unchanged.</p>}</Panel>
        : capabilities.data?.enabled ? <FleetWorkspace key={`${getApiBase()}:${auth.data?.username}:${auth.data?.groups?.join(',')}`} capabilities={capabilities.data} />
        : <p>Fleet Manager is not enabled on this instance.</p>}
    </main>
  </div>
}

function FleetWorkspace({ capabilities }: { capabilities: FleetCapabilities }) {
  const auth = useAuthMe()
  const queryClient = useQueryClient()
  const [fleetId, setFleetId] = useState('')
  const [tab, setTab] = useState<'clusters' | 'resources' | 'relationships' | 'operations' | 'manage'>('clusters')
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  const queryRoot = ['fleet', getApiBase(), auth.data?.username, auth.data?.groups]
  const inventory = useQuery({ queryKey: [...queryRoot, 'inventory'], queryFn: ({ signal }) => fleetQuery<FleetInventory>('list_clusters', {}, signal), retry: false, gcTime: 0 })
  const overview = useQuery({ queryKey: [...queryRoot, 'overview', fleetId], queryFn: ({ signal }) => fleetQuery<FleetOverview>('get_fleet_overview', fleetId ? { fleetId } : {}, signal), enabled: tab === 'clusters' && !!inventory.data && !inventory.error, retry: false, gcTime: 0 })
  const data = inventory.error ? undefined : inventory.data
  const clusters = (data?.clusters ?? []).filter(cluster => !fleetId || data?.memberships.some(member => member.fleetId === fleetId && member.clusterId === cluster.clusterId))
  const mutate = async (path: string, body?: unknown, method?: string) => {
    setBusy(true); setError(undefined)
    try {
      await fleetRequest(path, body, method)
      await queryClient.invalidateQueries({ queryKey: ['fleet', getApiBase()] })
      return true
    } catch (caught) { setError(caught); return false } finally { setBusy(false) }
  }
  return <>
    <div className="flex flex-wrap items-end justify-between gap-4">
      <label className="block text-sm space-y-1">Fleet scope<select className={inputClass} value={fleetId} onChange={event => setFleetId(event.target.value)}><option value="">All configured clusters</option>{data?.fleets.map(fleet => <option key={fleet.id} value={fleet.id}>{fleet.name}</option>)}</select></label>
      <Button variant="outline" onClick={() => void queryClient.invalidateQueries({ queryKey: ['fleet', getApiBase()] })} disabled={inventory.isFetching}><RefreshCw className="h-4 w-4" />Refresh observations</Button>
    </div>
    <nav aria-label="Fleet sections" className="flex flex-wrap gap-2 border-b border-theme-border pb-3">
      {(['clusters', 'resources', 'relationships', 'operations', 'manage'] as const).map(value => <Button key={value} variant={tab === value ? 'default' : 'ghost'} aria-current={tab === value ? 'page' : undefined} onClick={() => setTab(value)}>{value === 'manage' ? 'Fleet & connections' : value === 'resources' ? 'Resources & evidence' : value === 'relationships' ? 'Relationships & topology' : value === 'operations' ? 'Operations & audit' : 'Cluster overview'}</Button>)}
    </nav>
    <ErrorNotice error={inventory.error ?? error} />
    {inventory.isPending && <p role="status">Reading the configured catalog…</p>}
    {data && <>
      <p className="text-sm text-theme-text-secondary">{data.note}</p>
      {tab === 'clusters' && <>
        <ErrorNotice error={overview.error} />
        {overview.isFetching && <p role="status" className="text-sm text-theme-text-tertiary">Collecting bounded, caller-authorized observations…</p>}
        {clusters.length === 0 && <Panel title="No clusters in this scope"><p className="text-sm text-theme-text-secondary">Add trusted local connections or update deployment configuration, then select Fleet members.</p></Panel>}
        <div className="grid gap-4 lg:grid-cols-2">{clusters.map(cluster => {
          const observation = overview.error ? undefined : overview.data?.clusters.find(row => row.clusterId === cluster.clusterId)
          return <Panel key={cluster.clusterId} title={cluster.name}>
            <div className="flex flex-wrap gap-2"><State value={cluster.state} /><State value={`identity_${cluster.identity}`} /></div>
            <dl className="text-xs grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-theme-text-secondary"><dt>Cluster ID</dt><dd className="font-mono break-all select-all">{cluster.clusterId}</dd><dt>Connection</dt><dd>{cluster.connectionId}</dd><dt>Scoped assessment</dt><dd>{(observation?.assessmentState ?? observation?.health)?.replaceAll('_', ' ') ?? 'Not assessed'}</dd><dt>Observation</dt><dd>{observation?.observedAt ? new Date(observation.observedAt).toLocaleString() : 'No current resource observation'}</dd></dl>
            {observation && <details className="text-sm"><summary className="cursor-pointer text-theme-text-secondary">Assessment evidence and coverage</summary><div className="space-y-3 mt-3"><p className="text-xs text-theme-text-tertiary">Only collected Pod, Deployment and Node observations are assessed. Missing fields and denied or incomplete reads cannot establish whole-cluster health.</p><Limitations truncated={observation.assessmentTruncated} /><div className="flex flex-wrap gap-3 text-xs">{observation.assessmentCoverage?.map(coverage => <span key={coverage.resource}>{coverage.resource}: <State value={coverage.state} /></span>)}</div>{observation.findings?.map((finding, index) => <div key={`${finding.code}:${index}`} className="border-t border-theme-border pt-3"><p><State value={finding.severity} /> {finding.summary || finding.code}</p><ul className="text-xs text-theme-text-tertiary mt-2 break-all">{finding.evidence.map((evidence, i) => <li key={i}>{evidence}</li>)}</ul></div>)}{observation.findings?.length === 0 && <p className="text-xs text-theme-text-secondary">No finding returned in this collection; inspect assessment coverage before drawing conclusions.</p>}</div></details>}
            {observation && <div className="overflow-x-auto"><table className="w-full text-sm text-left"><thead className="text-theme-text-tertiary"><tr><th className="py-2">Resource</th><th>Count</th><th>Coverage</th></tr></thead><tbody>{observation.resources.map(row => <tr key={row.resource} className="border-t border-theme-border"><td className="py-2">{row.resource}</td><td>{coverageCount(row)}</td><td><State value={row.state} />{row.namespaces?.filter(ns => ns.state !== 'complete').map(ns => <div className="text-xs mt-1" key={ns.namespace}>{ns.namespace}: {ns.state}</div>)}</td></tr>)}</tbody></table></div>}
            <DuplicateReview cluster={cluster} data={data} canManage={capabilities.canManage} busy={busy} mutate={mutate} />
            {capabilities.canManage && <Button variant="outline" size="sm" disabled={busy} onClick={() => void mutate(`/clusters/${encodeURIComponent(cluster.clusterId)}/refresh`, {})}>Reconnect this cluster</Button>}
          </Panel>
        })}</div>
        <p className="text-xs text-theme-text-tertiary">Counts are per connection, not unique physical-cluster totals. Partial counts are lower bounds; unknown or denied is never zero.</p>
      </>}
      {tab === 'manage' && <FleetManagement data={data} capabilities={capabilities} busy={busy} mutate={mutate} />}
      {tab === 'resources' && <FleetResources key={fleetId} fleetId={fleetId} clusters={clusters} queryRoot={queryRoot} />}
      {tab === 'relationships' && <FleetRelationships key={fleetId} fleetId={fleetId} clusters={clusters} queryRoot={queryRoot} />}
      {tab === 'operations' && <FleetOperations key={fleetId} clusters={clusters} enabled={capabilities.writesEnabled} queryRoot={queryRoot} />}
    </>}
  </>
}

type Mutate = (path: string, body?: unknown, method?: string) => Promise<boolean>

function DuplicateReview({ cluster, data, canManage, busy, mutate }: { cluster: FleetCluster; data: FleetInventory; canManage: boolean; busy: boolean; mutate: Mutate }) {
  return <>{cluster.duplicateCandidates?.map(other => {
    const decision = data.duplicateDecisions?.some(item => (item.clusterId === cluster.clusterId && item.otherClusterId === other) || (item.clusterId === other && item.otherClusterId === cluster.clusterId))
    return <div key={other} className="text-sm text-warning-text space-y-2"><p>Matching identity evidence with {data.clusters.find(item => item.clusterId === other)?.name ?? other}. Credentials and visibility remain separate.</p>{decision ? <p className="text-theme-text-tertiary">Reviewed: keep separate.</p> : canManage && <Button variant="outline" size="sm" disabled={busy} onClick={() => void mutate('/duplicates', { clusterId: cluster.clusterId, otherClusterId: other, decision: 'keep_separate' })}>Review: keep separate</Button>}</div>
  })}</>
}

function FleetManagement({ data, capabilities, busy, mutate }: { data: FleetInventory; capabilities: FleetCapabilities; busy: boolean; mutate: Mutate }) {
  const [editing, setEditing] = useState<FleetRecord | null>(null)
  const [id, setId] = useState('')
  const [name, setName] = useState('')
  const [members, setMembers] = useState<string[]>([])
  const [deleting, setDeleting] = useState<string>()
  const selectFleet = (fleet: FleetRecord | null) => {
    setEditing(fleet); setId(fleet?.id ?? ''); setName(fleet?.name ?? '')
    setMembers(data.memberships.filter(member => member.fleetId === fleet?.id).map(member => member.clusterId)); setDeleting(undefined)
  }
  const locked = !capabilities.canManage || !!editing?.declarative
  return <div className="grid gap-5 lg:grid-cols-2">
    <Panel title="Fleet membership">
      <div className="flex flex-wrap gap-2"><Button variant="outline" onClick={() => selectFleet(null)}>New Fleet</Button>{data.fleets.map(fleet => <Button key={fleet.id} variant={editing?.id === fleet.id ? 'default' : 'outline'} onClick={() => selectFleet(fleet)}>{fleet.name}</Button>)}</div>
      {!capabilities.canManage && <p className="text-sm text-theme-text-secondary">Catalog changes require the instance operator's Fleet administration grant. Kubernetes permissions are separate.</p>}
      {editing?.declarative && <p className="text-sm text-theme-text-secondary">Deployment-managed Fleet. Edit its source configuration instead.</p>}
      <form className="space-y-3" onSubmit={event => { event.preventDefault(); void mutate(`/fleets/${encodeURIComponent(id)}`, { name, clusterIds: members }, 'PUT') }}>
        <label className="block text-sm space-y-1">Fleet ID<input className={inputClass} required pattern="[A-Za-z0-9][A-Za-z0-9._-]{0,63}" maxLength={64} value={id} disabled={locked || !!editing} onChange={event => setId(event.target.value)} /></label>
        <label className="block text-sm space-y-1">Display name<input className={inputClass} required maxLength={1024} value={name} disabled={locked} onChange={event => setName(event.target.value)} /></label>
        <fieldset disabled={locked || busy} className="space-y-2"><legend className="text-sm mb-2">Member clusters</legend>{data.clusters.map(cluster => <label key={cluster.clusterId} className="flex gap-2 text-sm"><input type="checkbox" checked={members.includes(cluster.clusterId)} onChange={event => setMembers(previous => event.target.checked ? [...previous, cluster.clusterId] : previous.filter(member => member !== cluster.clusterId))} /><span>{cluster.name}<span className="text-theme-text-tertiary"> · {cluster.connectionId}</span></span></label>)}</fieldset>
        <Button type="submit" disabled={locked || busy || !id || !name}>Save membership</Button>
      </form>
      {editing && !locked && (deleting === editing.id ? <div className="space-y-2 text-sm"><p>Remove this Fleet grouping? Kubernetes resources and connections will not be deleted.</p><Button variant="outline" disabled={busy} onClick={() => void mutate(`/fleets/${encodeURIComponent(editing.id)}`, undefined, 'DELETE').then(ok => { if (ok) selectFleet(null) })}>Confirm remove Fleet</Button><Button variant="ghost" onClick={() => setDeleting(undefined)}>Cancel</Button></div> : <Button variant="outline" disabled={busy} onClick={() => setDeleting(editing.id)}>Remove Fleet</Button>)}
    </Panel>
    <Panel title="Connection enrollment">
      {capabilities.localConnections && capabilities.canManage ? <LocalConnections data={data} busy={busy} mutate={mutate} /> : <p className="text-sm text-theme-text-secondary">Connections are managed by deployment configuration. Local discovery is an explicit operator option; this page never accepts credentials or arbitrary kubeconfig paths.</p>}
    </Panel>
  </div>
}

function LocalConnections({ data, busy, mutate }: { data: FleetInventory; busy: boolean; mutate: Mutate }) {
  const auth = useAuthMe()
  const candidates = useQuery({ queryKey: ['fleet', getApiBase(), auth.data?.username, auth.data?.groups, 'candidates'], queryFn: ({ signal }) => fleetRequest<{ candidates: FleetCandidate[]; note: string }>('/candidates', undefined, 'GET', signal), retry: false, gcTime: 0 })
  const [candidateId, setCandidateId] = useState('')
  const [id, setId] = useState('')
  const [name, setName] = useState('')
  const [removing, setRemoving] = useState<string>()
  return <>
    <ErrorNotice error={candidates.error} />
    <p className="text-sm text-theme-text-secondary">{candidates.data?.note ?? 'Only contexts discovered from trusted local configuration can be enrolled.'}</p>
    <form className="space-y-3" onSubmit={event => { event.preventDefault(); void mutate('/connections', { candidateId, id, name }) }}>
      <label className="block text-sm space-y-1">Discovered context<select className={inputClass} required value={candidateId} onChange={event => setCandidateId(event.target.value)}><option value="">Choose a context</option>{!candidates.error && candidates.data?.candidates.map(candidate => <option key={candidate.id} value={candidate.id}>{candidate.name} · {candidate.sourceLabel}</option>)}</select></label>
      <label className="block text-sm space-y-1">Connection ID<input className={inputClass} required pattern="[A-Za-z0-9][A-Za-z0-9._-]{0,63}" maxLength={64} value={id} onChange={event => setId(event.target.value)} /></label>
      <label className="block text-sm space-y-1">Display name<input className={inputClass} required maxLength={1024} value={name} onChange={event => setName(event.target.value)} /></label>
      <Button type="submit" disabled={busy || !!candidates.error || !candidateId}>Enroll connection</Button>
    </form>
    {data.clusters.filter(cluster => data.managedConnectionIds?.includes(cluster.connectionId)).map(cluster => <div key={cluster.clusterId} className="border-t border-theme-border pt-3 space-y-2"><p className="text-sm">{cluster.name}</p>{removing === cluster.connectionId ? <><p className="text-sm text-warning-text">Disconnect this runtime? No Kubernetes resources will be deleted.</p><Button variant="outline" disabled={busy} onClick={() => void mutate(`/connections/${encodeURIComponent(cluster.connectionId)}`, undefined, 'DELETE').then(ok => { if (ok) setRemoving(undefined) })}>Confirm disconnect</Button><Button variant="ghost" onClick={() => setRemoving(undefined)}>Cancel</Button></> : <Button variant="outline" size="sm" disabled={busy} onClick={() => setRemoving(cluster.connectionId)}>Disconnect</Button>}</div>)}
  </>
}

function FleetResources({ fleetId, clusters, queryRoot }: { fleetId: string; clusters: FleetCluster[]; queryRoot: unknown[] }) {
  const [clusterId, setClusterId] = useState('')
  const [resource, setResource] = useState('pods')
  const [namespace, setNamespace] = useState('')
  const [nameContains, setNameContains] = useState('')
  const [input, setInput] = useState<Record<string, unknown>>()
  const [selected, setSelected] = useState<FleetResource>()
  const results = useQuery({ queryKey: [...queryRoot, 'search', input], queryFn: ({ signal }) => fleetQuery<FleetSearch>('search_fleet_resources', input, signal), enabled: !!input, retry: false, gcTime: 0 })
  const resetObservation = () => { setInput(undefined); setSelected(undefined) }
  return <Panel title="Cross-cluster resource search">
    <form className="flex flex-wrap items-end gap-3" onSubmit={event => { event.preventDefault(); setSelected(undefined); const next = { ...(fleetId ? { fleetId } : {}), ...(clusterId ? { clusterIds: [clusterId] } : {}), resource, namespace, nameContains, limit: 100 }; if (JSON.stringify(input) === JSON.stringify(next)) void results.refetch(); else setInput(next) }}>
      <label className="text-sm space-y-1">Target cluster<select className={inputClass} value={clusterId} onChange={event => { setClusterId(event.target.value); resetObservation() }}><option value="">All clusters in scope</option>{clusters.map(cluster => <option key={cluster.clusterId} value={cluster.clusterId}>{cluster.name} · {cluster.connectionId}</option>)}</select></label>
      <label className="text-sm space-y-1">Resource<select className={inputClass} value={resource} onChange={event => { setResource(event.target.value); setNamespace(''); resetObservation() }}>{['pods', 'deployments', 'services', 'nodes', 'namespaces'].map(kind => <option key={kind}>{kind}</option>)}</select></label>
      <label className="text-sm space-y-1">Namespace<input className={inputClass} value={namespace} disabled={['nodes', 'namespaces'].includes(resource)} onChange={event => { setNamespace(event.target.value); resetObservation() }} placeholder="Configured accessible scope" /></label>
      <label className="text-sm space-y-1">Name contains<input className={inputClass} value={nameContains} onChange={event => { setNameContains(event.target.value); resetObservation() }} /></label>
      <Button type="submit" disabled={results.isFetching}><Search className="w-4 h-4" />Search</Button>
    </form>
    <ErrorNotice error={results.error} />
    {results.isFetching && <p role="status">Searching selected clusters…</p>}
    {!results.error && results.data && <>
      <div className="flex flex-wrap gap-3">{results.data.coverage.map(row => <div key={row.clusterId} className="text-xs text-theme-text-secondary">{clusters.find(cluster => cluster.clusterId === row.clusterId)?.name ?? row.clusterId}: <State value={row.collection.state} />{row.collection.namespaces?.map(ns => <span key={ns.namespace} className="ml-2">{ns.namespace}: {ns.state}</span>)}</div>)}</div>
      {results.data.truncated && <p className="text-sm text-warning-text">Results are truncated. Narrow the cluster, resource, namespace or name filters.</p>}
      <div className="overflow-x-auto"><table className="w-full text-sm text-left"><thead><tr><th className="py-2">Cluster / connection</th><th>Kind</th><th>Namespace</th><th>Name</th></tr></thead><tbody>{results.data.resources.map(item => <tr key={`${item.clusterId}:${item.uid}:${item.ref.kind}:${item.ref.name}`} className="border-t border-theme-border"><td className="py-3">{clusters.find(cluster => cluster.clusterId === item.clusterId)?.name ?? item.clusterId}<div className="text-xs text-theme-text-tertiary">{item.connectionId}</div></td><td>{item.ref.kind}</td><td>{item.ref.namespace || 'Cluster scoped'}</td><td><button className="text-accent hover:underline text-left" onClick={() => setSelected(item)}>{item.ref.name}</button></td></tr>)}</tbody></table></div>
      {results.data.resources.length === 0 && <p className="text-sm text-theme-text-secondary">No matches in the collected scope. Check coverage before concluding the resource is absent.</p>}
    </>}
    {selected && <div className="border-t border-theme-border pt-4 text-sm space-y-4"><h3 className="font-medium">Resource identity</h3><Provenance target={selected} /><FleetEvidence key={`${selected.clusterId}:${selected.uid}`} target={selected} queryRoot={queryRoot} /><Button variant="ghost" size="sm" onClick={() => setSelected(undefined)}>Close resource</Button></div>}
  </Panel>
}
