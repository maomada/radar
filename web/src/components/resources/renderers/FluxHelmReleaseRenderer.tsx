import { FluxHelmReleaseRenderer as BaseFluxHelmReleaseRenderer, type FluxValuesReference } from '@skyhook-io/k8s-ui/components/resources/renderers/FluxHelmReleaseRenderer'
import { ResourceLink } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { isForbiddenError, isNotFoundError, useResource } from '../../../api/client'

interface FluxHelmReleaseData {
  metadata?: { namespace?: string }
  spec?: { valuesFrom?: FluxValuesReference[] }
}

export function FluxHelmReleaseRenderer({ data, onNavigate }: {
  data: FluxHelmReleaseData
  onNavigate?: (ref: ResourceRef) => void
}) {
  return <BaseFluxHelmReleaseRenderer
    data={data}
    onNavigate={onNavigate}
    renderValuesReference={reference => <ValuesReference
      reference={reference}
      namespace={data.metadata?.namespace ?? ''}
      onNavigate={onNavigate}
    />}
  />
}

function ValuesReference({ reference, namespace, onNavigate }: {
  reference: FluxValuesReference
  namespace: string
  onNavigate?: (ref: ResourceRef) => void
}) {
  const kind = reference.kind === 'Secret' ? 'secrets' : 'configmaps'
  const query = useResource<{ metadata: { name: string } }>(kind, namespace, reference.name)

  if (query.data && !query.error) {
    return <ResourceLink name={reference.name} kind={kind} namespace={namespace} onNavigate={onNavigate} />
  }

  const missing = isNotFoundError(query.error)
  const message = query.isLoading ? 'Checking availability…'
    : missing ? reference.optional ? 'Not found - skipped by Flux' : 'Not found - required by Flux'
    : isForbiddenError(query.error) ? 'Access denied - cannot check availability'
    : 'Could not check availability'

  return <>
    <span className="text-theme-text-secondary">{reference.name}</span>
    <span className={missing && !reference.optional ? 'text-red-400 text-xs' : 'text-theme-text-tertiary text-xs'}>{message}</span>
    {!query.isLoading && <button
      type="button"
      onClick={() => { void query.refetch() }}
      disabled={query.isFetching}
      aria-label={`Check ${reference.name} again`}
      className="text-xs text-theme-text-secondary hover:text-theme-text-primary disabled:opacity-50"
    >Check again</button>}
  </>
}
