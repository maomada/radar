// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { FleetEvidence } from './FleetEvidence'

const { query } = vi.hoisted(() => ({ query: vi.fn() }))
vi.mock('../../api/fleet', async importOriginal => ({
  ...await importOriginal<typeof import('../../api/fleet')>(),
  fleetQuery: (...args: unknown[]) => query(...args),
}))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const target = { clusterId: 'cluster-b', connectionId: 'connection-b', ref: { kind: 'Pod', namespace: 'team', name: 'same-name' }, uid: 'pod-b-uid', apiVersion: 'v1', resourceVersion: '7', source: 'kubernetes-api', observedAt: '2026-10-27T12:00:00Z' }
let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let client: QueryClient
const button = (label: string) => [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === label)!
beforeEach(() => {
  query.mockReset()
  query.mockResolvedValue({ target, state: 'complete', object: { metadata: { name: 'same-name' } }, limitations: [] })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host)
})
afterEach(() => { act(() => root.unmount()); client.clear(); host.remove() })
async function render() {
  await act(async () => root.render(<QueryClientProvider client={client}><FleetEvidence target={target} queryRoot={['evidence-test']} /></QueryClientProvider>))
}
async function settled(text: string) {
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(host.textContent).toContain(text)
  })
}
it('reads only on demand with explicit cluster and UID, retaining provenance', async () => {
  await render()
  expect(query).not.toHaveBeenCalled()
  act(() => button('Read resource').click())
  await settled('pod-b-uid')
  expect(query).toHaveBeenCalledWith('get_fleet_resource', { clusterId: 'cluster-b', resource: 'pods', namespace: 'team', name: 'same-name', uid: 'pod-b-uid' }, expect.any(AbortSignal))
  expect(host.textContent).toContain('connection-b')
})
it('withholds a response for a replaced resource rather than attributing it to the old UID', async () => {
  query.mockResolvedValue({ target: { ...target, uid: 'replacement-uid' }, state: 'complete', object: { unsafeAttribution: 'must-not-display' }, limitations: [] })
  await render()
  act(() => button('Read resource').click())
  await settled('resource identity changed')
  expect(host.textContent).not.toContain('must-not-display')
})
it('requires an explicit container before fetching logs', async () => {
  await render()
  act(() => button('Read pod logs').click())
  expect(query).not.toHaveBeenCalled()
  expect(button('Fetch bounded logs').disabled).toBe(true)
  expect(host.textContent).toContain('Redaction is best effort')
})
