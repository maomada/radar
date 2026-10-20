// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { NodeDrainStatus } from './NodeDrainStatus'
import { useDrainNode, nodeDrainActivityKey, type NodeDrainActivity } from '../../api/client'
import { setApiBase } from '../../api/config'

let host: HTMLDivElement
let root: Root
let client: QueryClient
let start: () => Promise<unknown>
let respond: (response: Response) => void
let reject: (error: Error) => void

function Harness({ uid = 'node-uid', visible = true }: { uid?: string; visible?: boolean }) {
  const mutation = useDrainNode(uid)
  start = () => mutation.mutateAsync({ name: 'worker' }).catch(() => undefined)
  return visible ? <NodeDrainStatus name="worker" nodeUID={uid} /> : null
}

async function render(uid = 'node-uid', visible = true) {
  await act(async () => {
    root.render(<QueryClientProvider client={client}><Harness uid={uid} visible={visible} /></QueryClientProvider>)
  })
}

async function flush() {
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
}

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  setApiBase('/api')
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve, fail) => { respond = resolve; reject = fail })))
})

afterEach(() => {
  act(() => root.unmount())
  client.clear()
  host.remove()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  setApiBase('/api')
})

it('retains completion when the view changes before the request finishes', async () => {
  await render()
  expect(host.textContent).toBe('')
  let pending: Promise<unknown> | undefined
  await act(async () => { pending = start() })
  await flush()
  expect(host.textContent).toContain('Drain in progress')
  expect(host.textContent).toContain('2m 0s')
  await render('', false)
  await act(async () => {
    respond(Response.json({ outcome: 'complete', evictedPods: ['shop/web'], waitedForDeletion: true }))
    await pending
  })
  await flush()
  await render()
  expect(host.textContent).toContain('Drain complete: 1 pod removed')
  expect(host.textContent).toContain('Last drain in this tab:')
  expect(host.textContent).toContain('The node was left cordoned.')
  expect(host.textContent).not.toContain('The node remains cordoned.')
  expect(host.querySelector('time')?.dateTime).toBeTruthy()
  expect(fetch).toHaveBeenCalledTimes(1)
  await act(async () => { pending = start() })
  await flush()
  expect(host.textContent).toContain('Drain in progress')
  await act(async () => { respond(Response.json({ outcome: 'complete' })); await pending })
  await flush()
  expect(host.textContent).toContain('Nothing to evict')
})

it('retains pending state on return and isolates node UID and API base', async () => {
  await render()
  let pending: Promise<unknown> | undefined
  await act(async () => { pending = start() })
  await flush()
  await render('replacement-uid')
  expect(host.textContent).toBe('')
  setApiBase('/c/other/api')
  await render()
  expect(host.textContent).toBe('')
  setApiBase('/api')
  await render()
  expect(host.textContent).toContain('Drain in progress')
  await act(async () => {
    respond(Response.json({ outcome: 'incomplete', skippedPods: [{ namespace: 'shop', name: 'bare', expectedSkip: false, reason: 'protected' }] }))
    await pending
  })
  await flush()
  expect(host.textContent).toContain('Drain incomplete: 1 pod left on the node')
})

it('preserves request failure as uncertainty without inheriting automatic mutation retries', async () => {
  vi.stubGlobal('crypto', undefined)
  client.setDefaultOptions({ mutations: { retry: 1 } })
  await render()
  let pending: Promise<unknown> | undefined
  await act(async () => { pending = start() })
  await act(async () => { reject(new Error('Connection lost')); await pending })
  await flush()
  expect(host.textContent).toContain('Could not confirm drain completion')
  expect(host.textContent).toContain('Connection lost')
  expect(host.textContent).toContain('does not confirm whether evictions ran')
  expect(fetch).toHaveBeenCalledTimes(1)
})

it('dismisses the recorded result and retains it past the normal cache lifetime until then', async () => {
  await render()
  let pending: Promise<unknown> | undefined
  await act(async () => { pending = start() })
  await act(async () => { respond(Response.json({ outcome: 'complete' })); await pending })
  await flush()
  await render('', false)
  vi.useFakeTimers()
  await act(async () => { await vi.advanceTimersByTimeAsync(10 * 60 * 1000) })
  await render()
  expect(host.textContent).toContain('Nothing to evict')
  await act(async () => { host.querySelector<HTMLButtonElement>('button')?.click(); await vi.advanceTimersByTimeAsync(0) })
  expect(host.textContent).toBe('')
  await render('', false)
  await render()
  expect(host.textContent).toBe('')
})

it('does not recreate a result after the context cache was cleared', async () => {
  await render()
  let pending: Promise<unknown> | undefined
  await act(async () => { pending = start() })
  await render('', false)
  client.removeQueries()
  await act(async () => { respond(Response.json({ outcome: 'complete' })); await pending })
  await render()
  expect(host.textContent).toBe('')
})

it('does not let an older response replace the latest drain result', async () => {
  await render()
  let first: Promise<unknown> | undefined
  let second: Promise<unknown> | undefined
  await act(async () => { first = start() })
  const respondFirst = respond
  await act(async () => { second = start() })
  await act(async () => {
    respond(Response.json({ outcome: 'incomplete', pendingPods: ['shop/web'] }))
    await second
  })
  await flush()
  await act(async () => {
    respondFirst(Response.json({ outcome: 'complete' }))
    await first
  })
  await flush()
  expect(host.textContent).toContain('Drain incomplete: 1 pod left on the node')
  expect(host.textContent).not.toContain('Nothing to evict')
})

it('shows elapsed time and overdue response guidance without inventing a completed outcome', async () => {
  vi.useFakeTimers()
  const activity: NodeDrainActivity = { status: 'pending', startedAt: Date.now(), timeoutSeconds: 120, requestId: 1 }
  client.setQueryData(nodeDrainActivityKey('/api', 'worker', 'node-uid'), activity)
  await render()
  await act(async () => { await vi.advanceTimersByTimeAsync(126_000) })
  expect(host.textContent).toContain('Elapsed 2m 6s')
  expect(host.textContent).toContain("Still waiting for Radar's response")
  expect(host.textContent).toContain('Drain in progress')
  expect(host.querySelector('button')).toBeNull()
})
