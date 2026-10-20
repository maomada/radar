// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { CloudInstallStage } from '../api/client'
import { CloudConnectFlow } from './CloudConnectFlow'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root

beforeEach(() => {
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})

afterEach(async () => {
  await act(async () => root.unmount())
  document.body.replaceChildren()
})

async function renderPreparing(prepareStep?: CloudInstallStage) {
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <CloudConnectFlow
          status={{ state: 'preparing', prepareStep }}
          blocked={null}
          exit={{ href: 'https://cloud.example', label: 'Continue in Radar Cloud', install: false }}
          onStatus={() => {}}
          onExit={() => {}}
        />
      </QueryClientProvider>,
    )
  })
}

// Each row reads as done (check), active (spinner) or todo (dot).
function rows() {
  return Array.from(document.querySelectorAll('li')).map(li => {
    const icon = li.querySelector('svg')
    const state = !icon ? 'todo' : icon.classList.contains('animate-spin') ? 'active' : 'done'
    return `${state}: ${li.textContent}`
  })
}

describe('Cloud connect preparation steps', () => {
  it.each([
    [undefined, ['active', 'todo', 'todo']],
    ['inspect', ['active', 'todo', 'todo']],
    ['prepare', ['done', 'active', 'todo']],
    ['preflight', ['done', 'done', 'active']],
  ] as const)('step %s marks the rows %j', async (step, states) => {
    await renderPreparing(step)
    expect(rows()).toEqual([
      `${states[0]}: Looking for an existing Radar install`,
      `${states[1]}: Preparing the Radar Helm chart`,
      `${states[2]}: Checking your permissions with a dry run`,
    ])
    expect(document.body.textContent).toContain('Checking this cluster')
    expect(document.body.textContent).toContain('Nothing is installed in this step.')
  })
})
