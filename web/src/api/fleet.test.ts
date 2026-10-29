// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { coverageCount, fleetQuery, fleetRequest } from './fleet'
import { setApiBase, setAuthHeadersProvider, setCredentialsMode } from './config'

afterEach(() => {
  vi.unstubAllGlobals()
  setApiBase('/api')
  setAuthHeadersProvider(() => ({}))
  setCredentialsMode('same-origin')
})

describe('Fleet transport and coverage', () => {
  it('preserves explicit cluster selection and host transport configuration', async () => {
    setApiBase('/embedded/radar/api')
    setAuthHeadersProvider(() => ({ 'X-Test-Identity': 'reader' }))
    setCredentialsMode('include')
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ resources: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetch)
    const input = { clusterIds: ['cluster-b'], resource: 'pods' }
    await fleetQuery('search_fleet_resources', input)
    expect(fetch).toHaveBeenCalledWith('/embedded/radar/api/fleet/query/search_fleet_resources', expect.objectContaining({ credentials: 'include', method: 'POST', headers: { 'X-Test-Identity': 'reader', 'Content-Type': 'application/json' }, body: JSON.stringify(input) }))
  })

  it('preserves an authorization failure rather than returning empty resources', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: 'Access denied' }), { status: 403, headers: { 'Content-Type': 'application/json' } })))
    await expect(fleetRequest('/capabilities')).rejects.toMatchObject({ status: 403, message: 'Access denied' })
  })

  it('shows unknown and partial counts without claiming exact zero', () => {
    const base = { resource: 'pods', countScope: 'configured-and-caller-visible' }
    expect(coverageCount({ ...base, state: 'denied' })).toBe('Unknown')
    expect(coverageCount({ ...base, state: 'partial', count: 0 })).toBe('≥ 0')
    expect(coverageCount({ ...base, state: 'complete', count: 0 })).toBe('0')
  })
})
