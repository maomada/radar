// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi, type MockInstance } from 'vitest'
import { useBulkDeleteResources, useDeleteResource } from './client'

let success: MockInstance
let notice: MockInstance
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
beforeEach(() => {
  success = vi.spyOn(console, 'log').mockImplementation(() => {})
  notice = vi.spyOn(console, 'warn').mockImplementation(() => {})
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.restoreAllMocks()
})

async function remove(responses: Response[], bulk: boolean, count = 1, namespace = 'shop') {
  const fetchMock = vi.fn<typeof fetch>(async () => responses.shift()!)
  vi.stubGlobal('fetch', fetchMock)
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  let run: (() => Promise<unknown>) | undefined
  function Probe() {
    const single = useDeleteResource()
    const many = useBulkDeleteResources()
    const items = Array.from({ length: count }, (_, i) => ({ kind: 'configmaps', namespace, name: i ? `app-${i}` : 'app' }))
    run = () => bulk ? many.mutateAsync({ items }) : single.mutateAsync(items[0])
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  let failure: unknown
  await act(async () => { try { await run!() } catch (err) { failure = err } })
  act(() => root.unmount())
  client.clear()
  expect(fetchMock).toHaveBeenCalledTimes(count)
  expect(fetchMock.mock.calls[0]?.[0]).toContain(`/api/resources/configmaps/${namespace || "_"}/app`)
  return failure
}


for (const bulk of [false,true]) {
 const mode=bulk?'bulk':'single'
 it(`${mode}: confirms completed deletion from the current JSON contract`,async()=>{
  expect(await remove([new Response('{}',{status:200})],bulk)).toBeUndefined()
  expect(success).toHaveBeenCalledExactlyOnceWith('[Toast]',bulk?'1 resource deleted':'Resource deleted',undefined)
  expect(notice).not.toHaveBeenCalled()
 })
 for(const body of ['', 'not JSON', 'null', '[]', 'true', '{"pendingFinalizers":[1]}', '{"observationError":false}']) {
  it(`${mode}: an accepted malformed response is unconfirmed`,async()=>{
   expect(await remove([new Response(body,{status:200})],bulk)).toBeUndefined()
   expect(success).not.toHaveBeenCalled()
   expect(JSON.stringify(notice.mock.calls)).toContain('did not confirm cleanup')
  })
 }
 it(`${mode}: an accepted body-read failure is unconfirmed`,async()=>{
  const response = new Response('{}', {status:200})
  vi.spyOn(response, 'text').mockRejectedValue(new Error('response stream interrupted'))
  expect(await remove([response],bulk)).toBeUndefined()
  expect(success).not.toHaveBeenCalled()
  expect(JSON.stringify(notice.mock.calls)).toContain('did not confirm cleanup')
 })
 for(const status of [403,409,500]) {
  it(`${mode}: rejects HTTP ${status}`,async()=>{
   expect(await remove([new Response(JSON.stringify({error:'request rejected'}),{status})],bulk)).toBeInstanceOf(Error)
   expect(success).not.toHaveBeenCalled();expect(notice).not.toHaveBeenCalled()
  })
 }
 for(const result of [{deletionTimestamp:'now'}, {deletionTimestamp:'now',pendingFinalizers:['example.com/hold']}, {observationError:'no get permission'}]) {
  it(`${mode}: accepted asynchronous or unconfirmed deletion stays progress`,async()=>{
   expect(await remove([new Response(JSON.stringify(result))],bulk)).toBeUndefined()
   expect(success).not.toHaveBeenCalled();expect(notice).toHaveBeenCalled()
   expect(JSON.stringify(notice.mock.calls)).not.toContain('force delete')
  })
 }
 it(`${mode}: cluster-scoped URLs use the namespace placeholder`,async()=>{
  expect(await remove([new Response('{}')],bulk,1,'')).toBeUndefined()
 })
}
it('bulk prioritizes pending finalizers while retaining unconfirmed item details',async()=>{
 expect(await remove([new Response(JSON.stringify({pendingFinalizers:['hold'],deletionTimestamp:'now'})),new Response(JSON.stringify({observationError:'read denied'}))],true,2)).toBeUndefined()
 expect(success).not.toHaveBeenCalled()
 expect(JSON.stringify(notice.mock.calls)).toContain('waiting on finalizers')
 expect(JSON.stringify(notice.mock.calls)).toContain('read denied')
})
