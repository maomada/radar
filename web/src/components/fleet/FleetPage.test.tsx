// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import FleetPage from './FleetPage'

const state = vi.hoisted(() => ({
  auth: { authEnabled: true, username: 'reader', groups: ['team'], authMode: 'oidc' },
  canManage: false,
  query: vi.fn(),
}))
vi.mock('../../api/client', () => ({ useAuthMe: () => ({ data: state.auth, isPending: false }) }))
vi.mock('../../api/fleet', async importOriginal => ({
  ...await importOriginal<typeof import('../../api/fleet')>(),
  useFleetCapabilities: () => ({ data: { enabled: true, canManage: state.canManage, localConnections: false, writesEnabled: false }, isPending: false }),
  fleetQuery: (...args: unknown[]) => state.query(...args),
}))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let client: QueryClient
const button = (label: string) => [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === label)!

beforeEach(() => {
  state.auth.username = 'reader'
  state.canManage = false
  state.query.mockReset()
  const clusters = ['a', 'b'].map(id => ({ clusterId: `cluster-${id}`, connectionId: `connection-${id}`, name: `Cluster ${id}`, state: 'ready', identity: 'verified', kinds: [] }))
  state.query.mockImplementation(async (tool: string) => {
    if (tool === 'list_clusters') return { clusters, fleets: [], memberships: [], managedConnectionIds: [], duplicateDecisions: [], note: 'Fixture catalog' }
    if (tool === 'get_fleet_overview') return { clusters: clusters.map(cluster => ({ ...cluster, health: 'not_assessed', resources: [{ resource: 'pods', state: 'denied', countScope: 'configured' }] })) }
    if (tool === 'search_fleet_resources') return {
      resources: [{ clusterId: 'cluster-b', connectionId: 'connection-b', ref: { kind: 'Pod', namespace: 'team', name: 'same-name' }, uid: 'pod-b-uid', resourceVersion: '7', source: 'kubernetes-api', observedAt: '2026-10-27T12:00:00Z' }],
      coverage: [{ clusterId: 'cluster-b', collection: { state: 'complete' } }], truncated: false,
    }
    throw new Error(`Unexpected tool ${tool}`)
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

async function render() {
  await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><FleetPage /></QueryClientProvider></MemoryRouter>))
}

async function ready() {
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(host.textContent).toContain('Fixture catalog')
  })
}

it('keeps unauthenticated users out of inventory queries', async () => {
  state.auth.username = ''
  await render()
  expect(host.textContent).toContain('Authentication required')
  expect(state.query).not.toHaveBeenCalled()
})

it('shows denied observations as unknown rather than healthy or zero', async () => {
  await render()
  await ready()
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(host.textContent).toContain('Unknown')
    expect(host.textContent).toContain('denied')
  })
  expect(host.textContent).toContain('not assessed')
})

it('keeps Kubernetes read access separate from catalog administration', async () => {
  await render()
  await ready()
  act(() => button('Fleet & connections').click())
  expect(button('Save membership').disabled).toBe(true)
  expect(host.textContent).toContain('Kubernetes permissions are separate')
})

it('searches the explicit selected cluster and retains its resource identity', async () => {
  await render()
  await ready()
  act(() => button('Resources & evidence').click())
  const target = host.querySelector('select option[value="cluster-b"]')!.parentElement as HTMLSelectElement
  act(() => { target.value = 'cluster-b'; target.dispatchEvent(new Event('change', { bubbles: true })) })
  act(() => button('Search').click())
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(button('same-name')).toBeDefined()
  })
  expect(state.query).toHaveBeenCalledWith('search_fleet_resources', expect.objectContaining({ clusterIds: ['cluster-b'], resource: 'pods' }), expect.any(AbortSignal))
  act(() => button('same-name').click())
  expect(host.textContent).toContain('pod-b-uid')
  expect(host.textContent).toContain('connection-b')
})
