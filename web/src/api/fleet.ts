import { useQuery } from '@tanstack/react-query'
import { apiUrl, getApiBase, getAuthHeaders, getCredentialsMode } from './config'
import { readErrorResponse } from './httpErrors'

export interface FleetCapabilities {
  enabled: boolean
  canManage: boolean
  localConnections: boolean
  writesEnabled: boolean
}

export interface FleetCluster {
  clusterId: string
  connectionId: string
  name: string
  state: string
  identity: string
  duplicateCandidates?: string[]
  lastAttemptAt?: string
  lastReadyAt?: string
  kinds: { kind: string; probe: string; readiness: string }[]
}

export interface FleetRecord {
  id: string
  name: string
  declarative?: boolean
}

export interface FleetInventory {
  clusters: FleetCluster[]
  fleets: FleetRecord[]
  memberships: { fleetId: string; clusterId: string }[]
  managedConnectionIds: string[]
  duplicateDecisions: { clusterId: string; otherClusterId: string; decision: string }[]
  note: string
}

export interface FleetCoverage {
  resource: string
  state: string
  count?: number
  countScope: string
  scope?: string
  namespaces?: { namespace: string; state: string }[]
}

export interface FleetOverview {
  clusters: {
    clusterId: string
    connectionId: string
    health: string
    observedAt: string
    source: string
    resources: FleetCoverage[]
    assessmentState: string
    findings: FleetFinding[]
    assessmentCoverage: FleetCoverage[]
    assessmentTruncated: boolean
  }[]
  note: string
}

export interface FleetResource {
  clusterId: string
  connectionId: string
  ref: { group?: string; kind: string; namespace?: string; name: string }
  uid: string
  apiVersion: string
  resourceVersion: string
  observedAt: string
  source: string
}

export interface FleetSearch {
  resources: FleetResource[]
  coverage: { clusterId: string; observedAt: string; source: string; collection: FleetCoverage }[]
  truncated: boolean
}

export interface FleetCandidate {
  id: string
  name: string
  context: string
  sourceLabel: string
}

export interface FleetFinding {
  code: string
  severity: string
  summary: string
  evidence: string[]
}

export interface FleetEvidence {
  target: FleetResource
  state: string
  object?: Record<string, unknown>
  limitations: string[]
}

export interface FleetEvents extends FleetEvidence {
  events: { uid: string; type: string; reason: string; message: string; count: number; lastObservedAt: string }[]
  truncated: boolean
}

export interface FleetLogs extends FleetEvidence {
  container: string
  text: string
  truncated: boolean
}

export interface FleetDiagnosis extends FleetEvidence {
  findings: FleetFinding[]
}

export interface FleetRelationship {
  id: string
  type: string
  sourceClusterId: string
  targetClusterId?: string
  targetCandidates?: string[]
  state: string
  memberName?: string
  sourceResource?: FleetResource
  targetResource?: FleetResource
  evidence: FleetResource[]
}

export interface FleetRelationships {
  relationships: FleetRelationship[]
  coverage: { clusterId: string; resource: string; state: string }[]
  truncated: boolean
  limitations: string[]
}

export interface FleetTopology {
  nodes: { id: string; clusterId?: string; label: string; type: string; ref?: FleetResource['ref']; uid?: string }[]
  edges: { id: string; source: string; target: string; type: string; state: string; evidence: FleetResource[] }[]
  coverage: FleetRelationships['coverage']
  truncated: boolean
  limitations: string[]
}

export interface FleetOperationPreview {
  operationId: string
  clusterId: string
  connectionId: string
  action: string
  target: FleetResource
  confirmation: string
  expiresAt: string
  summary: string[]
  dryRun: boolean
  manifestDigest?: string
  limitations: string[]
}

export interface FleetOperationResult {
  operationId: string
  clusterId: string
  state: string
  summary: string
  limitations: string[]
}

export interface FleetAudit {
  records: {
    operationId: string
    clusterId: string
    connectionId: string
    action: string
    resource: string
    namespace: string
    name: string
    stage: string
    outcome?: string
    observedAt: string
    actor: string
  }[]
  truncated: boolean
  limitations: string[]
}

export class FleetHTTPError extends Error {
  constructor(message: string, public status: number) { super(message) }
}

export async function fleetRequest<T>(path: string, body?: unknown, method = body === undefined ? 'GET' : 'POST', signal?: AbortSignal): Promise<T> {
  const response = await fetch(apiUrl(`/fleet${path}`), {
    method,
    credentials: getCredentialsMode(),
    headers: { ...getAuthHeaders(), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  })
  if (!response.ok) {
    const { body: error } = await readErrorResponse(response)
    throw new FleetHTTPError(error.error ?? `Fleet request failed (${response.status})`, response.status)
  }
  return response.json() as Promise<T>
}

export function fleetQuery<T>(tool: string, input: unknown, signal?: AbortSignal): Promise<T> {
  return fleetRequest<T>(`/query/${encodeURIComponent(tool)}`, input, 'POST', signal)
}

export function useFleetCapabilities(enabled = true, identity?: string) {
  return useQuery({
    queryKey: ['fleet', getApiBase(), identity, 'capabilities'],
    queryFn: ({ signal }) => fleetRequest<FleetCapabilities>('/capabilities', undefined, 'GET', signal),
    enabled,
    retry: false,
    staleTime: 0,
    gcTime: 0,
  })
}

export function coverageCount(coverage: FleetCoverage): string {
  if (coverage.count === undefined) return 'Unknown'
  return coverage.state === 'complete' ? String(coverage.count) : `≥ ${coverage.count}`
}
