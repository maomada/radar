// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { FleetRoute } from './FleetRoute'

const state = vi.hoisted(() => ({ embedded: false }))
vi.mock('../../context/NavCustomization', () => ({ useNavCustomization: () => state }))
vi.mock('./FleetPage', () => ({ default: () => <main>Fleet workspace fixture</main> }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
beforeEach(() => {
  state.embedded = false
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})
afterEach(() => {
  act(() => root.unmount())
  host.remove()
})

it.each([
  ['/fleet', false, 'Fleet workspace fixture'],
  ['/fleet', true, 'Existing single-cluster workspace'],
  ['/resources/pods?ctx=other-context', false, 'Existing single-cluster workspace'],
] as const)('preserves route isolation for %s with embedded=%s', async (path, embedded, expected) => {
  state.embedded = embedded
  await act(async () => root.render(<MemoryRouter initialEntries={[path]}><FleetRoute><main>Existing single-cluster workspace</main></FleetRoute></MemoryRouter>))
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(host.textContent).toBe(expected)
  })
})
