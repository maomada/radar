// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { FleetOperations } from './FleetOperations'

const { query } = vi.hoisted(() => ({ query: vi.fn() }))
vi.mock('../../api/fleet', async importOriginal => ({
  ...await importOriginal<typeof import('../../api/fleet')>(),
  fleetQuery: (...args: unknown[]) => query(...args),
}))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const clusters = ['a', 'b'].map(id => ({ clusterId: `cluster-${id}`, connectionId: `connection-${id}`, name: `Cluster ${id}`, identity: 'verified', state: 'ready', kinds: [] }))
let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let client: QueryClient
const button = (label: string) => [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === label)

beforeEach(() => {
  query.mockReset()
  query.mockImplementation(async (tool: string, input: Record<string, unknown>) => {
    if (tool === 'get_fleet_audit') return { records: [], truncated: false, limitations: [] }
    if (tool === 'prepare_fleet_operation') return { operationId: 'operation-1', clusterId: input.clusterId, connectionId: 'connection-b', action: input.action, dryRun: true, confirmation: 'opaque-once', expiresAt: new Date(Date.now() + 60000).toISOString(), summary: ['One workload will restart'], limitations: [], target: { clusterId: input.clusterId, connectionId: 'connection-b', ref: { group: 'apps', kind: 'Deployment', namespace: input.namespace, name: input.name }, uid: 'workload-uid', resourceVersion: '42', source: 'kubernetes-api', observedAt: new Date().toISOString() } }
    if (tool === 'execute_fleet_operation') return { operationId: 'operation-1', clusterId: input.clusterId, state: 'outcome_unknown', summary: 'The API outcome is unknown', limitations: ['Do not retry automatically'] }
    throw new Error(`Unexpected tool ${tool}`)
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host)
})
afterEach(() => { act(() => root.unmount()); client.clear(); host.remove() })
async function render(enabled = true) {
  await act(async () => root.render(<QueryClientProvider client={client}><FleetOperations enabled={enabled} clusters={clusters} queryRoot={['fleet-test']} /></QueryClientProvider>))
}
function change(label: string, value: string) {
  const element = [...host.querySelectorAll('label')].find(item => item.textContent?.startsWith(label))!.querySelector('input, select') as HTMLInputElement | HTMLSelectElement
  act(() => {
    const prototype = element.tagName === 'SELECT' ? HTMLSelectElement.prototype : HTMLInputElement.prototype
    Object.getOwnPropertyDescriptor(prototype, 'value')!.set!.call(element, value)
    element.dispatchEvent(new Event(element.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }))
  })
}
async function preview() {
  change('Target cluster', 'cluster-b'); change('Namespace', 'team'); change('Resource name', 'api')
  await act(async () => button('Prepare preview (no persistent change)')!.click())
  await vi.waitFor(() => expect(host.textContent).toContain('Review target and intended effect'))
}

it('keeps writes disabled and requires explicit cluster selection when enabled', async () => {
  await render(false)
  expect(host.querySelector('fieldset')!.disabled).toBe(true)
  expect(query.mock.calls.some(([tool]) => tool === 'prepare_fleet_operation')).toBe(false)
  await render(true)
  expect(button('Prepare preview (no persistent change)')!.disabled).toBe(true)
})

it('binds the preview to the selected cluster and clears confirmation after edits', async () => {
  await render(); await preview()
  expect(query).toHaveBeenCalledWith('prepare_fleet_operation', expect.objectContaining({ clusterId: 'cluster-b', namespace: 'team', name: 'api' }))
  expect(button('Confirm and execute once')!.disabled).toBe(true)
  change('Namespace', 'other')
  expect(button('Confirm and execute once')).toBeUndefined()
  expect(query.mock.calls.some(([tool]) => tool === 'execute_fleet_operation')).toBe(false)
})

it('executes once after explicit confirmation and never retries an unknown outcome', async () => {
  await render(); await preview()
  act(() => (host.querySelector('input[type="checkbox"]') as HTMLInputElement).click())
  await act(async () => {
    const confirm = button('Confirm and execute once')!
    confirm.click()
    confirm.click()
  })
  expect(query).toHaveBeenCalledWith('execute_fleet_operation', { clusterId: 'cluster-b', confirmation: 'opaque-once' })
  expect(query.mock.calls.filter(([tool]) => tool === 'execute_fleet_operation')).toHaveLength(1)
  expect(host.textContent).toContain('outcome unknown')
  expect(button('Confirm and execute once')).toBeUndefined()
})

it('withholds confirmation when the preview names a different action or resource', async () => {
  const implementation = query.getMockImplementation()!
  query.mockImplementation(async (tool: string, input: Record<string, unknown>) => {
    const response = await implementation(tool, input)
    return tool === 'prepare_fleet_operation' ? { ...response, action: 'apply' } : response
  })
  await render()
  change('Target cluster', 'cluster-b'); change('Namespace', 'team'); change('Resource name', 'api')
  await act(async () => button('Prepare preview (no persistent change)')!.click())
  expect(host.textContent).toContain('did not return a valid preview')
  expect(button('Confirm and execute once')).toBeUndefined()
  expect(query.mock.calls.some(([tool]) => tool === 'execute_fleet_operation')).toBe(false)
})
