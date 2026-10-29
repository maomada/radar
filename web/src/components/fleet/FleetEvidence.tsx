import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fleetQuery, type FleetDiagnosis, type FleetEvents, type FleetEvidence as EvidenceResult, type FleetLogs, type FleetResource } from '../../api/fleet'
import { Button, ErrorNotice, inputClass, State } from './FleetUI'

export const fleetResourcePlural: Record<string, string> = {
  Pod: 'pods', Deployment: 'deployments', StatefulSet: 'statefulsets', DaemonSet: 'daemonsets',
  ReplicaSet: 'replicasets', Service: 'services', Node: 'nodes', Namespace: 'namespaces',
}

export function Provenance({ target }: { target: FleetResource }) {
  return <dl className="grid grid-cols-[auto_1fr] gap-2 text-xs">{Object.entries({ Cluster: target.clusterId, Connection: target.connectionId || 'Not observed', Kind: target.ref.kind, 'API version': target.apiVersion || 'Not observed', Namespace: target.ref.namespace || '(cluster scoped)', Name: target.ref.name, UID: target.uid || 'Not observed', 'Resource version': target.resourceVersion || 'Not observed', Source: target.source, Observed: target.observedAt }).map(([key, value]) => <div key={key} className="contents"><dt className="text-theme-text-tertiary">{key}</dt><dd className="font-mono break-all">{value}</dd></div>)}</dl>
}

export function Limitations({ values, truncated }: { values?: string[]; truncated?: boolean }) {
  return <>{truncated && <p className="text-sm text-warning-text">The observation is truncated. Narrow the scope before drawing conclusions.</p>}{!!values?.length && <ul className="space-y-1 text-xs text-theme-text-secondary">{values.map((value, index) => <li key={index}>{value}</li>)}</ul>}</>
}

type View = 'resource' | 'events' | 'diagnosis' | 'logs'
const tools = { resource: 'get_fleet_resource', events: 'get_fleet_events', diagnosis: 'diagnose_fleet_resource', logs: 'get_fleet_logs' }

export function FleetEvidence({ target, queryRoot }: { target: FleetResource; queryRoot: unknown[] }) {
  const [view, setView] = useState<View>()
  const [container, setContainer] = useState('')
  const [previous, setPrevious] = useState(false)
  const [logInput, setLogInput] = useState<{ container: string; previous: boolean }>()
  const input = view === 'logs'
    ? { clusterId: target.clusterId, namespace: target.ref.namespace, name: target.ref.name, ...logInput, tailLines: 200 }
    : { clusterId: target.clusterId, resource: fleetResourcePlural[target.ref.kind], namespace: target.ref.namespace ?? '', name: target.ref.name, uid: target.uid, ...(view === 'events' ? { limit: 100 } : {}) }
  const result = useQuery({
    queryKey: [...queryRoot, 'evidence', target.clusterId, target.uid, view, input],
    queryFn: ({ signal }) => fleetQuery<EvidenceResult | FleetEvents | FleetDiagnosis | FleetLogs>(tools[view!], input, signal),
    enabled: !!view && (view !== 'logs' || !!logInput), retry: false, gcTime: 0,
  })
  const data = result.error || result.isFetching ? undefined : result.data
  const mismatch = data && (data.target.clusterId !== target.clusterId || (data.target.uid && data.target.uid !== target.uid))
  const truncated = data && 'truncated' in data && typeof data.truncated === 'boolean' ? data.truncated : undefined
  const events = data && 'events' in data && Array.isArray(data.events) ? data.events as FleetEvents['events'] : undefined
  const findings = data && 'findings' in data && Array.isArray(data.findings) ? data.findings as FleetDiagnosis['findings'] : undefined
  const logText = data && 'text' in data && typeof data.text === 'string' ? data.text : undefined
  return <div className="space-y-4">
    <div className="flex flex-wrap gap-2" aria-label="Resource evidence tools">{(['resource', 'events', 'diagnosis', ...(target.ref.kind === 'Pod' ? ['logs'] : [])] as View[]).map(item => <Button key={item} size="sm" variant={view === item ? 'default' : 'outline'} onClick={() => setView(item)}>{item === 'resource' ? 'Read resource' : item === 'events' ? 'Read events' : item === 'diagnosis' ? 'Diagnose observations' : 'Read pod logs'}</Button>)}</div>
    <p className="text-xs text-theme-text-tertiary">Evidence is read on demand using your identity on this exact cluster. Observations are not a root-cause verdict.</p>
    {view === 'logs' && <form className="space-y-3" onSubmit={event => { event.preventDefault(); setLogInput({ container: container.trim(), previous }) }}>
      <label className="block text-sm space-y-1">Container name<input className={inputClass} required value={container} onChange={event => { setContainer(event.target.value); setLogInput(undefined) }} /></label>
      <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={previous} onChange={event => { setPrevious(event.target.checked); setLogInput(undefined) }} />Previous container instance</label>
      <Button type="submit" disabled={!container.trim() || result.isFetching}>Fetch bounded logs</Button>
      <p className="text-xs text-warning-text">At most 200 lines / 64 KiB. Redaction is best effort; application logs can contain sensitive values.</p>
    </form>}
    <ErrorNotice error={result.error} />
    {result.isFetching && <p role="status" className="text-sm">Reading caller-authorized evidence…</p>}
    {mismatch ? <p role="alert" className="text-sm text-warning-text">The observed resource identity changed. Search again before using this evidence.</p> : data && (view !== 'logs' || logInput) && <>
      <State value={data.state} /><Provenance target={data.target} />
      <Limitations values={data.limitations} truncated={truncated} />
      {'object' in data && data.object && <pre className="max-h-96 overflow-auto rounded-lg bg-theme-elevated p-4 text-xs">{JSON.stringify(data.object, null, 2)}</pre>}
      {events && <div className="space-y-3">{events.map(event => <div key={event.uid} className="border-t border-theme-border pt-3 text-sm"><p><State value={event.type} /> {event.reason}</p><p className="whitespace-pre-wrap break-words mt-2">{event.message}</p><p className="text-xs text-theme-text-tertiary">{event.count} recorded occurrences · {event.lastObservedAt}</p></div>)}{events.length === 0 && <p className="text-sm text-theme-text-secondary">No events returned; check coverage and retention before concluding none occurred.</p>}</div>}
      {findings && <div className="space-y-3">{findings.map((finding, index) => <div key={`${finding.code}:${index}`} className="border-t border-theme-border pt-3 text-sm"><p><State value={finding.severity} /> {finding.summary}</p><p className="text-xs text-theme-text-tertiary mt-1">{finding.code}</p><ul className="mt-2 space-y-1 text-xs">{finding.evidence.map((evidence, i) => <li key={i}>{evidence}</li>)}</ul></div>)}{findings.length === 0 && <p className="text-sm text-theme-text-secondary">No finding in the collected observations. This does not establish whole-cluster health.</p>}</div>}
      {logText !== undefined && <pre className="max-h-96 overflow-auto rounded-lg bg-theme-elevated p-4 text-xs whitespace-pre-wrap break-words">{logText || 'No log text returned; check the observation state.'}</pre>}
    </>}
  </div>
}
