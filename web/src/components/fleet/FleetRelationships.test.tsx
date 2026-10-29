// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { FleetRelationships } from './FleetRelationships'

const query = vi.hoisted(() => vi.fn())
vi.mock('../../api/fleet', async importOriginal => ({
  ...await importOriginal<typeof import('../../api/fleet')>(),
  fleetQuery: (...args: unknown[]) => query(...args),
}))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let client: QueryClient
beforeEach(() => {
  query.mockReset()
  const coverage = [{ clusterId: 'hub', resource: 'clusters', state: 'denied' }]
  query.mockImplementation(async (tool: string) => tool === 'get_fleet_relationships' ? {
    relationships: [{ id: 'membership', type: 'member', sourceClusterId: 'hub', state: 'ambiguous', targetCandidates: ['a', 'b'], evidence: [] }],
    coverage, limitations: ['Declared identity is not physical identity proof.'], truncated: true,
  } : {
    nodes: [{ id: 'hub-node', clusterId: 'hub', type: 'cluster', label: 'Hub' }, { id: 'unresolved-member', type: 'unresolved', label: 'Unknown member' }],
    edges: [{ id: 'edge', source: 'hub-node', target: 'unresolved-member', type: 'member', state: 'unresolved', evidence: [] }],
    coverage, limitations: [], truncated: false,
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})
afterEach(() => {
  act(() => root.unmount())
  client.clear()
  host.remove()
})

it('keeps ambiguous targets, denied coverage and unresolved topology explicit within the selected Fleet', async () => {
  const clusters = ['hub', 'a', 'b'].map(id => ({ clusterId: id, connectionId: `connection-${id}`, name: `Cluster ${id}`, state: 'ready', identity: 'verified', kinds: [] }))
  await act(async () => root.render(<QueryClientProvider client={client}><FleetRelationships fleetId="selected-fleet" clusters={clusters} queryRoot={['fleet', 'reader']} /></QueryClientProvider>))
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(host.textContent).toContain('Ambiguous candidates: Cluster a (a); Cluster b (b). Kept separate.')
  })
  expect(host.textContent).toContain('denied')
  expect(host.textContent).toContain('Declared identity is not physical identity proof.')
  expect(query).toHaveBeenCalledWith('get_fleet_relationships', { fleetId: 'selected-fleet' }, expect.any(AbortSignal))
  const topology = [...host.querySelectorAll('button')].find(button => button.textContent === 'Topology')!
  await act(async () => topology.click())
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(host.textContent).toContain('Unknown member · Unresolved target')
  })
  expect(query).toHaveBeenCalledWith('get_fleet_topology', { fleetId: 'selected-fleet' }, expect.any(AbortSignal))
  expect(host.textContent).toContain('denied')
})
