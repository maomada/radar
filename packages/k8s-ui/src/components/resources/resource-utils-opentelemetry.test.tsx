import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { renderToString } from 'react-dom/server'
import { getCollectorPipelines, getOpenTelemetryCollectorStatus, getOpenTelemetryInstrumentationStatus } from './resource-utils-opentelemetry'
import { getGenericResourceStatus } from './generic-status'
import { getCellFilterValue } from './resource-utils'
import { ResourceRendererDispatch, getResourceStatus } from '../shared/ResourceRendererDispatch'

const vectors = JSON.parse(readFileSync(new URL('../../../../../pkg/health/testdata/opentelemetry.json', import.meta.url), 'utf8'))

describe('OpenTelemetry status across surfaces', () => {
  for (const vector of vectors) it(vector.name, () => {
    const resource = vector.object
    const kind = resource.kind === 'Instrumentation' ? 'instrumentations' : 'opentelemetrycollectors'
    const state = resource.kind === 'Instrumentation' ? getOpenTelemetryInstrumentationStatus(resource) : getOpenTelemetryCollectorStatus(resource)
    expect(state).toMatchObject({ text: vector.text, level: vector.level })
    expect(getGenericResourceStatus(resource)).toMatchObject({ text: vector.text, tone: vector.level })
    expect(getCellFilterValue(resource, 'status', kind)).toBe(vector.text)
    expect(getResourceStatus(kind, resource)).toMatchObject({ text: vector.text, level: vector.level })
  })
})

describe('Collector pipeline declarations', () => {
  const config = { connectors: { spanmetrics: {} }, service: { pipelines: { traces: { receivers: ['otlp'], processors: ['batch'], exporters: ['spanmetrics'] }, metrics: { receivers: ['spanmetrics'], exporters: ['debug'] } } } }
  it('reads beta structured and genuinely served alpha YAML configuration', () => {
    expect(getCollectorPipelines({ apiVersion: 'opentelemetry.io/v1beta1', spec: { config } })).toMatchObject({ connectors: ['spanmetrics'], pipelines: [{ name: 'traces', receivers: ['otlp'], processors: ['batch'], exporters: ['spanmetrics'] }, { name: 'metrics' }] })
    expect(getCollectorPipelines({ apiVersion: 'opentelemetry.io/v1alpha1', spec: { config: 'service:\n  pipelines:\n    traces:\n      receivers: [otlp]\n      exporters: [debug]\n' } }).pipelines).toHaveLength(1)
  })
  it('reports unavailable and malformed previews instead of a false empty configuration', () => {
    expect(getCollectorPipelines({ apiVersion: 'opentelemetry.io/v1alpha1', spec: { config: '[broken' } }).error).toContain('Inspect YAML')
    expect(getCollectorPipelines({ apiVersion: 'opentelemetry.io/v1beta1', spec: { config: 'service: {}' } }).error).toBeTruthy()
    expect(getCollectorPipelines({ spec: { config: { service: { pipelines: [] } } } }).error).toBeTruthy()
    expect(getCollectorPipelines({ spec: { config: {} } }).error).toBeUndefined()
  })
})

describe('OpenTelemetry drawer identity and meaning', () => {
  function render(apiVersion: string, kind: string, spec = {}) {
    return renderToString(<ResourceRendererDispatch resource={{ kind: kind === 'Instrumentation' ? 'instrumentations' : 'opentelemetrycollectors', namespace: 'test', name: 'test' }} data={{ apiVersion, kind, metadata: { name: 'test', namespace: 'test' }, spec }} onCopy={() => {}} copied={null} showCommonSections={false} />)
  }
  it('renders native reconciliation/pipeline context only for the exact group', () => {
    expect(render('opentelemetry.io/v1beta1', 'OpenTelemetryCollector')).toContain('Reconciliation')
    expect(render('other.example/v1', 'OpenTelemetryCollector')).not.toContain('Configured Pipelines')
    expect(render('other.example/v1', 'OpenTelemetryCollector', { custom: 'generic-visible' })).toContain('generic-visible')
  })
  it('does not claim instrumentation readiness or dump arbitrary env values', () => {
    const html = render('opentelemetry.io/v1alpha1', 'Instrumentation', { env: [{ name: 'TOKEN', value: 'do-not-surface' }] })
    expect(html).toContain('Configured')
    expect(html).toContain('does not report which Pods')
    expect(html).not.toContain('do-not-surface')
    expect(render('other.example/v1', 'Instrumentation', { custom: 'generic-visible' })).toContain('generic-visible')
  })
})
