import type { ReactNode } from 'react'
import { HardDrive } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink, useOperationalIssuesShown, type ConditionTone } from '../../ui/drawer-components'
import { formatAge } from '../resource-utils'

interface PVCRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  /** Optional host-provided section, used for a Prometheus-derived usage gauge. */
  extraSections?: ReactNode
}

const accessModeShorthand: Record<string, string> = {
  ReadWriteOnce: 'RWO',
  ReadOnlyMany: 'ROX',
  ReadWriteMany: 'RWX',
  ReadWriteOncePod: 'RWOP',
}

function formatAccessModes(modes: string[] | undefined): string | undefined {
  if (!modes || modes.length === 0) return undefined
  return modes.map(m => accessModeShorthand[m] || m).join(', ')
}

function pvcConditionTone(condition: any): ConditionTone | undefined {
  if (condition?.type !== 'Unused') return undefined
  return condition.status === 'True' ? 'unknown' : condition.status === 'False' ? 'ok' : 'unknown'
}

export function PVCRenderer({ data, onNavigate, extraSections }: PVCRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const annotations = data.metadata?.annotations || {}
  const phase = status.phase

  // Lost is a genuine failure (bound volume disappeared). Pending is a normal
  // lifecycle state (provisioning / WaitForFirstConsumer), surfaced calmly below.
  const isLost = phase === 'Lost'
  const isPending = phase === 'Pending'

  // Provisioner info from annotations
  const provisioner = annotations['volume.kubernetes.io/storage-provisioner']
  const selectedNode = annotations['volume.kubernetes.io/selected-node']
  const bindCompleted = annotations['pv.kubernetes.io/bind-completed']
  const hasProvisionerInfo = provisioner || selectedNode || bindCompleted
  const operationalIssuesShown = useOperationalIssuesShown()
  const unused = Array.isArray(status.conditions)
    ? status.conditions.find((condition: any) => condition?.type === 'Unused' && condition.status === 'True')
    : undefined

  return (
    <>
      {/* Problem alerts */}
      {isLost && !operationalIssuesShown && (
        <AlertBanner
          variant="error"
          title="发现问题"
          message="PVC 已丢失其绑定的 Volume"
        />
      )}

      {isPending && (
        <AlertBanner
          variant="info"
          title="Pending — 尚未绑定"
          message="PVC 在 Volume 配置期间会保持 Pending；使用 WaitForFirstConsumer StorageClass 时，则会等到挂载它的 Pod 被调度后再绑定。如果一直处于 Pending，请检查 StorageClass、配置器和存储配额。"
        />
      )}

      {unused && (
        <AlertBanner
          variant="info"
          title={unused.lastTransitionTime ? `Unused for about ${formatAge(unused.lastTransitionTime)}` : 'Unused'}
          message="当前没有非终止状态的 Pod 引用此存储声明。删除前请确认保留策略和数据归属。"
        />
      )}

      <Section title="状态" icon={HardDrive}>
        <PropertyList>
          <Property
            label="Phase"
            value={
              <span className={clsx(
                phase === 'Bound' && 'text-green-400',
                phase === 'Pending' && 'text-yellow-400',
                phase === 'Lost' && 'text-red-400',
              )}>
                {phase}
              </span>
            }
          />
          <Property label="容量" value={status.capacity?.storage} />
          <Property label="请求量" value={spec.resources?.requests?.storage} />
          <Property label="Storage Class" value={
            spec.storageClassName ? <ResourceLink name={spec.storageClassName} kind="storageclasses" namespace="" onNavigate={onNavigate} /> : undefined
          } />
          <Property label="访问模式" value={formatAccessModes(spec.accessModes)} />
          <Property label="Volume 模式" value={spec.volumeMode} />
          <Property label="Volume 名称" value={
            spec.volumeName ? <ResourceLink name={spec.volumeName} kind="persistentvolumes" namespace="" onNavigate={onNavigate} /> : undefined
          } />
        </PropertyList>
      </Section>

      {hasProvisionerInfo && (
        <Section title="配置器信息">
          <PropertyList>
            <Property label="Provisioner" value={provisioner} />
            <Property label="Selected Node" value={selectedNode} />
            <Property label="Bind Completed" value={bindCompleted} />
          </PropertyList>
        </Section>
      )}

      {extraSections}

      <ConditionsSection conditions={status.conditions} getConditionTone={pvcConditionTone} />
    </>
  )
}
