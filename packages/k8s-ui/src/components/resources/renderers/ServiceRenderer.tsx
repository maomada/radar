import { type ReactNode } from 'react'
import { Globe, Clock, Radio } from 'lucide-react'
import { Section, PropertyList, Property, KeyValueBadgeList, CopyHandler, AlertBanner } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'

export interface ServicePortRenderProps {
  namespace: string
  serviceName: string
  port: number
  protocol: string
  name?: string
  appProtocol?: string
}

interface ServiceRendererProps {
  data: any
  onCopy: CopyHandler
  copied: string | null
  endpointSlices?: any[]
  endpointSlicesLoading?: boolean
  endpointSlicesEnabled?: boolean
  endpointSlicesError?: string
  endpointSlicesTruncated?: boolean
  onNavigate?: (ref: ResourceRef) => void
  renderPortAction?: (props: ServicePortRenderProps) => ReactNode
  renderPortPanel?: (props: ServicePortRenderProps) => ReactNode
}

function endpointSliceAddressCount(slice: any): number {
  return (slice.endpoints || []).reduce((total: number, endpoint: any) => total + (endpoint.addresses?.length || 0), 0)
}

function endpointSliceReadyCount(slice: any): number {
  return (slice.endpoints || []).filter((endpoint: any) => endpoint?.conditions?.ready !== false).length
}

function endpointSliceReadyClass(ready: number, total: number): string {
  if (total === 0) return 'status-unknown'
  if (ready === total) return 'status-healthy'
  if (ready > 0) return 'status-degraded'
  return 'status-unhealthy'
}

export function ServiceRenderer({ data, onCopy, copied, endpointSlices, endpointSlicesLoading, endpointSlicesEnabled, endpointSlicesError, endpointSlicesTruncated, onNavigate, renderPortAction, renderPortPanel }: ServiceRendererProps) {
  const spec = data.spec || {}
  const ports = spec.ports || []
  const lbIngress = data.status?.loadBalancer?.ingress || []
  const namespace = data.metadata?.namespace

  const isLoadBalancer = spec.type === 'LoadBalancer'
  const isExternalName = spec.type === 'ExternalName'
  const lbPending = isLoadBalancer && lbIngress.length === 0
  const hasNoSelector = !spec.selector || Object.keys(spec.selector).length === 0

  return (
    <>
      {/* LoadBalancer pending warning */}
      {lbPending && (
        <AlertBanner
          variant="warning"
          icon={Clock}
          title="Load Balancer 等待中"
          message="尚未分配外部 IP/主机名，可能需要几分钟。如果配置过程停滞，请查看下方事件。"
        />
      )}

      {/* No selector warning (manual endpoints) */}
      {hasNoSelector && !isExternalName && (
        <AlertBanner
          variant="info"
          title="没有 Pod 选择器"
          message="此 Service 没有选择器 — 端点必须手动管理或由外部控制器管理。"
        />
      )}

      <Section title="Service" icon={Globe}>
        <PropertyList>
          <Property label="类型" value={spec.type || 'ClusterIP'} />
          {isExternalName ? (
            <Property label="外部名称" value={spec.externalName} copyable onCopy={onCopy} copied={copied} />
          ) : (
            <Property label="集群 IP" value={spec.clusterIP} copyable onCopy={onCopy} copied={copied} />
          )}
          {spec.externalIPs?.length > 0 && (
            <Property label="外部 IP" value={spec.externalIPs.join(', ')} copyable onCopy={onCopy} copied={copied} />
          )}
          {lbIngress.map((ing: any, i: number) => (
            <Property
              key={i}
              label={lbIngress.length > 1 ? `Load Balancer ${i + 1}` : 'Load Balancer'}
              value={ing.ip || ing.hostname}
              copyable
              onCopy={onCopy}
              copied={copied}
            />
          ))}
          <Property label="会话亲和性" value={spec.sessionAffinity} />
          <Property label="外部流量" value={spec.externalTrafficPolicy} />
          <Property label="内部流量" value={spec.internalTrafficPolicy} />
          {spec.ipFamilyPolicy && <Property label="IP Family Policy" value={spec.ipFamilyPolicy} />}
          {spec.ipFamilies?.length > 0 && <Property label="IP Families" value={spec.ipFamilies.join(', ')} />}
        </PropertyList>
      </Section>

      {ports.length > 0 && (
        <Section title="端口" defaultExpanded>
          <ServicePortCards service={data} renderPortAction={renderPortAction} renderPortPanel={renderPortPanel} />
        </Section>
      )}

      {spec.selector && (
        <Section title="选择器">
          <KeyValueBadgeList items={spec.selector} />
        </Section>
      )}

      {!isExternalName && (hasNoSelector || endpointSlicesEnabled) && (
        <Section title="EndpointSlices" icon={Radio} defaultExpanded>
          {endpointSlicesLoading ? (
            <div className="text-sm text-theme-text-tertiary">正在加载 EndpointSlices…</div>
          ) : endpointSlicesError ? (
            <AlertBanner variant="warning" title="EndpointSlice 清单不可用" message={endpointSlicesError} />
          ) : endpointSlices && endpointSlices.length > 0 ? (
            <div className="space-y-2">
              {endpointSlices.map((slice: any) => {
                const sliceName = slice.metadata?.name
                const endpoints = slice.endpoints || []
                const ready = endpointSliceReadyCount(slice)
                const addresses = endpointSliceAddressCount(slice)
                return (
                  <button
                    key={slice.metadata?.uid || sliceName}
                    type="button"
                    className="card-inner w-full text-left hover:bg-theme-hover transition-colors"
                    onClick={() => onNavigate?.({
                      kind: 'EndpointSlice',
                      group: 'discovery.k8s.io',
                      namespace,
                      name: sliceName,
                    })}
                  >
                    <div className="flex items-center justify-between gap-3">
                      <div className="min-w-0">
                        <div className="text-sm font-medium text-theme-text-primary truncate">{sliceName}</div>
                        <div className="text-xs text-theme-text-tertiary mt-0.5">{slice.addressType || 'Unknown'} address type</div>
                      </div>
                      <div className="flex flex-wrap items-center justify-end gap-1.5 shrink-0">
                        <span className={`badge-sm ${endpointSliceReadyClass(ready, endpoints.length)}`}>{ready}/{endpoints.length} ready</span>
                        <span className="badge-sm bg-theme-elevated text-theme-text-secondary border border-theme-border">{addresses} addresses</span>
                      </div>
                    </div>
                  </button>
                )
              })}
              {endpointSlicesTruncated && (
                <div className="text-xs text-theme-text-tertiary">Showing the first {endpointSlices.length} EndpointSlices.</div>
              )}
            </div>
          ) : (
            <div className="text-sm text-theme-text-tertiary">此 Service 未找到 EndpointSlice。</div>
          )}
        </Section>
      )}
    </>
  )
}

export function ServicePortCards({
  service,
  renderPortAction,
  renderPortPanel,
}: {
  service: any
  renderPortAction?: (props: ServicePortRenderProps) => ReactNode
  renderPortPanel?: (props: ServicePortRenderProps) => ReactNode
}) {
  const ports = service?.spec?.ports || []
  const namespace = service?.metadata?.namespace || ''
  const serviceName = service?.metadata?.name || ''
  return (
    <div className="space-y-2">
      {ports.map((port: any, i: number) => {
        const props: ServicePortRenderProps = {
          namespace,
          serviceName,
          port: port.port,
          protocol: port.protocol || 'TCP',
          name: port.name,
          appProtocol: port.appProtocol,
        }
        return (
          <div key={`${serviceName}-${port.name || i}-${port.port}-${port.protocol || 'TCP'}`} className="card-inner text-sm">
            <div className="flex items-center justify-between gap-2">
              <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
                <span className="font-medium text-theme-text-primary">{port.name || `port-${i + 1}`}</span>
                <span className="text-xs text-theme-text-tertiary">{port.protocol || 'TCP'}</span>
                <span className="font-mono text-xs text-theme-text-secondary">
                  {port.port}{port.targetPort != null && port.targetPort !== port.port ? ` → ${port.targetPort}` : ''}
                  {port.nodePort ? ` (NodePort: ${port.nodePort})` : ''}
                </span>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {renderPortAction?.(props)}
              </div>
            </div>
            {renderPortPanel?.(props)}
          </div>
        )
      })}
    </div>
  )
}
