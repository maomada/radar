import { useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fleetQuery, type FleetAudit, type FleetCluster, type FleetOperationPreview, type FleetOperationResult } from '../../api/fleet'
import { Button, ErrorNotice, inputClass, Panel, State } from './FleetUI'
import { fleetResourcePlural, Limitations, Provenance } from './FleetEvidence'

interface Draft {
  clusterId: string
  action: 'restart' | 'scale' | 'apply'
  resource: string
  namespace: string
  name: string
  replicas: string
  manifest: string
}

export function FleetOperations({ clusters, enabled, queryRoot }: { clusters: FleetCluster[]; enabled: boolean; queryRoot: unknown[] }) {
  const [draft, setDraft] = useState<Draft>({ clusterId: '', action: 'restart', resource: 'deployments', namespace: '', name: '', replicas: '1', manifest: '' })
  const [preview, setPreview] = useState<FleetOperationPreview>()
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState<'prepare' | 'execute'>()
  const [error, setError] = useState<unknown>()
  const [result, setResult] = useState<FleetOperationResult>()
  const inFlight = useRef(false)
  const selected = clusters.find(cluster => cluster.clusterId === draft.clusterId)
  const eligible = enabled && selected?.identity === 'verified' && ['ready', 'partial'].includes(selected.state)
  const resourceOptions = draft.action === 'restart' ? ['deployments', 'statefulsets', 'daemonsets'] : draft.action === 'scale' ? ['deployments', 'statefulsets'] : ['deployments', 'statefulsets', 'daemonsets', 'replicasets', 'pods', 'services']
  const update = (values: Partial<Draft>) => { setDraft({ ...draft, ...values }); setPreview(undefined); setConfirmed(false); setResult(undefined); setError(undefined) }
  const prepare = async () => {
    if (!eligible || inFlight.current) return
    inFlight.current = true
    setBusy('prepare'); setError(undefined); setResult(undefined); setPreview(undefined); setConfirmed(false)
    try {
      const input = { clusterId: draft.clusterId, action: draft.action, resource: draft.resource, namespace: draft.namespace.trim(), name: draft.name.trim(), ...(draft.action === 'scale' ? { replicas: Number(draft.replicas) } : {}), ...(draft.action === 'apply' ? { manifest: draft.manifest } : {}) }
      const next = await fleetQuery<FleetOperationPreview>('prepare_fleet_operation', input)
      const group = ['pods', 'services'].includes(draft.resource) ? '' : 'apps'
      if (!next.dryRun || !next.confirmation || next.action !== draft.action || next.clusterId !== draft.clusterId || next.connectionId !== selected?.connectionId || next.target.clusterId !== draft.clusterId || next.target.connectionId !== next.connectionId || (next.target.ref.group ?? '') !== group || fleetResourcePlural[next.target.ref.kind] !== draft.resource || next.target.ref.namespace !== input.namespace || next.target.ref.name !== input.name || !Number.isFinite(Date.parse(next.expiresAt))) throw new Error('The server did not return a valid preview for this explicit target. Nothing was executed.')
      setPreview(next)
    } catch (caught) { setError(caught) } finally { inFlight.current = false; setBusy(undefined) }
  }
  const execute = async () => {
    if (!preview || !confirmed || !eligible || inFlight.current) return
    if (Date.parse(preview.expiresAt) <= Date.now()) { setPreview(undefined); setConfirmed(false); setError(new Error('Preview expired. Prepare a new preview before confirming.')); return }
    const approved = preview
    inFlight.current = true
    setPreview(undefined); setConfirmed(false); setError(undefined); setBusy('execute')
    try {
      setResult(await fleetQuery<FleetOperationResult>('execute_fleet_operation', { clusterId: approved.clusterId, confirmation: approved.confirmation }))
    } catch {
      setError(new Error(`No acknowledged result for operation ${approved.operationId}. Its outcome may be unknown; inspect the audit trail and target cluster before preparing any new operation. It will not be retried automatically.`))
    } finally { inFlight.current = false; setBusy(undefined) }
  }
  return <div className="space-y-6"><Panel title="Explicit cluster operation">
    <p className="text-sm text-theme-text-secondary">Each operation targets one cluster and one resource. Preview uses the target API server's dry run under your Kubernetes identity; Fleet administration is not permission to change workloads.</p>
    {!enabled && <p className="text-sm text-warning-text">Fleet writes are disabled by deployment configuration. Audit history remains read-only.</p>}
    <form onSubmit={event => { event.preventDefault(); void prepare() }}>
      <fieldset disabled={!enabled || !!busy} className="space-y-4 disabled:opacity-60">
        <div className="grid gap-3 md:grid-cols-2">
          <label className="block text-sm space-y-1">Target cluster<select className={inputClass} required value={draft.clusterId} onChange={event => update({ clusterId: event.target.value })}><option value="">Choose an explicit cluster</option>{clusters.map(cluster => <option key={cluster.clusterId} value={cluster.clusterId}>{cluster.name} · {cluster.clusterId}</option>)}</select></label>
          <label className="block text-sm space-y-1">Action<select className={inputClass} value={draft.action} onChange={event => update({ action: event.target.value as Draft['action'], resource: 'deployments' })}><option value="restart">Restart rollout</option><option value="scale">Scale replicas</option><option value="apply">Apply manifest</option></select></label>
          <label className="block text-sm space-y-1">Resource<select className={inputClass} value={draft.resource} onChange={event => update({ resource: event.target.value })}>{resourceOptions.map(resource => <option key={resource}>{resource}</option>)}</select></label>
          <label className="block text-sm space-y-1">Namespace<input className={inputClass} value={draft.namespace} required onChange={event => update({ namespace: event.target.value })} /></label>
          <label className="block text-sm space-y-1">Resource name<input className={inputClass} value={draft.name} required onChange={event => update({ name: event.target.value })} /></label>
          {draft.action === 'scale' && <label className="block text-sm space-y-1">Desired replicas<input type="number" min={0} max={10000} step={1} className={inputClass} value={draft.replicas} required onChange={event => update({ replicas: event.target.value })} /></label>}
        </div>
        {draft.action === 'apply' && <label className="block text-sm space-y-1">Single workload or Service manifest<textarea className={`${inputClass} min-h-48 font-mono`} required maxLength={262144} spellCheck={false} value={draft.manifest} onChange={event => update({ manifest: event.target.value })} /><span className="block text-xs text-theme-text-tertiary">The kind, namespace and name must match the selected target. No Secrets, RBAC, CRDs, Lists or cluster-scoped resources. The server accepts at most 256 KiB; the preview does not echo manifest values.</span></label>}
        <Button type="submit" disabled={!eligible || !draft.name.trim() || !draft.namespace.trim() || !!busy}>{busy === 'prepare' ? 'Preparing server dry run…' : 'Prepare preview (no persistent change)'}</Button>
      </fieldset>
    </form>
    {selected && !eligible && enabled && <p className="text-sm text-warning-text">This cluster is not a routable verified identity. Resolve its connection or identity before writing.</p>}
    <ErrorNotice error={error} />
    {preview && <section className="space-y-4 border-t border-theme-border pt-4" aria-label="Operation preview">
      <h3 className="text-sm font-medium">Review target and intended effect</h3><Provenance target={preview.target} />
      <ul className="text-sm space-y-1">{preview.summary.map((item, index) => <li key={index}>{item}</li>)}</ul>
      <p className="text-xs text-theme-text-secondary">Preview expires: {new Date(preview.expiresAt).toLocaleString()} · Operation {preview.operationId}</p>
      {preview.manifestDigest && <p className="font-mono text-xs break-all">Manifest digest: {preview.manifestDigest}</p>}
      <Limitations values={preview.limitations} />
      <label className="flex items-start gap-2 text-sm"><input className="mt-1" type="checkbox" checked={confirmed} onChange={event => setConfirmed(event.target.checked)} /><span>I confirm <strong>{preview.action}</strong> on <strong>{selected?.name} ({preview.clusterId})</strong> → {preview.target.ref.namespace}/{preview.target.ref.name}. This can change running workloads.</span></label>
      <Button disabled={!confirmed || !eligible || !!busy} onClick={() => void execute()}>Confirm and execute once</Button>
    </section>}
    {busy === 'execute' && <p role="status" className="text-sm text-warning-text">Submitting once to the confirmed cluster. Do not repeat the operation while its outcome is pending.</p>}
    {result && <div className="space-y-2 border-t border-theme-border pt-4"><State value={result.state} /><p className="text-sm">{result.summary}</p><p className="text-xs font-mono break-all">{result.clusterId} · Operation {result.operationId}</p><Limitations values={result.limitations} /><p className="text-xs text-theme-text-secondary">An acknowledged API change is not proof the workload converged. Inspect fresh resource evidence before further action.</p></div>}
  </Panel><FleetAuditHistory clusters={clusters} queryRoot={queryRoot} /></div>
}

function FleetAuditHistory({ clusters, queryRoot }: { clusters: FleetCluster[]; queryRoot: unknown[] }) {
  const [clusterId, setClusterId] = useState('')
  const result = useQuery({ queryKey: [...queryRoot, 'audit', clusterId], queryFn: ({ signal }) => fleetQuery<FleetAudit>('get_fleet_audit', { ...(clusterId ? { clusterId } : {}), limit: 100 }, signal), retry: false, gcTime: 0 })
  const data = result.error || result.isFetching ? undefined : result.data
  return <Panel title="Audit trail">
    <p className="text-xs text-theme-text-secondary">Your operations are visible here; Fleet administrators can inspect other callers' records. An intent without an acknowledged result is unresolved, not a failed or successful change.</p>
    <div className="flex flex-wrap gap-3"><label className="text-sm flex-1">Cluster<select className={inputClass} value={clusterId} onChange={event => setClusterId(event.target.value)}><option value="">All authorized audit records</option>{clusters.map(cluster => <option key={cluster.clusterId} value={cluster.clusterId}>{cluster.name} · {cluster.clusterId}</option>)}</select></label><Button variant="outline" disabled={result.isFetching} onClick={() => void result.refetch()}>Refresh audit</Button></div>
    <ErrorNotice error={result.error} />{result.isFetching && <p role="status" className="text-sm">Reading audit records…</p>}
    {data && <><Limitations values={data.limitations} truncated={data.truncated} /><div className="overflow-x-auto"><table className="w-full text-xs text-left"><thead><tr><th className="py-2">Time / operation</th><th>Cluster / resource</th><th>Actor / action</th><th>Recorded stage</th></tr></thead><tbody>{data.records.map((record, index) => <tr key={`${record.operationId}:${index}`} className="border-t border-theme-border"><td className="py-3">{new Date(record.observedAt).toLocaleString()}<div className="font-mono">{record.operationId}</div></td><td className="break-all">{record.clusterId}<div>{record.resource} {record.namespace}/{record.name}</div></td><td>{record.actor}<div>{record.action}</div></td><td><State value={record.stage} />{record.outcome && <div className="mt-1"><State value={record.outcome} /></div>}</td></tr>)}</tbody></table></div>{data.records.length === 0 && <p className="text-sm text-theme-text-secondary">No readable records returned.</p>}</>}
  </Panel>
}
