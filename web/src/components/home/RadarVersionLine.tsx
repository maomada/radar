import { ArrowUpCircle, Megaphone } from 'lucide-react'
import { gitOpsRouteForResource } from '@skyhook-io/k8s-ui'
import type { CloudConnectSelf, VersionInfo } from '../../api/client'
import {
  getVersionUpdateStatus,
  IN_CLUSTER_UPGRADE_URL,
  type VersionUpdateTier,
} from '../../utils/version'
import { Tooltip } from '../ui/Tooltip'

interface RadarVersionLineProps {
  version: VersionInfo
  manager?: CloudConnectSelf
  managerLoading?: boolean
  onNavigateToHelmRelease?: (namespace: string, release: string) => void
  onNavigateToGitOps?: (path: string) => void
  // Local installs upgrade through the update notification, and this line's
  // upgrade link points at in-cluster instructions.
  showUpgrade?: boolean
  onShowWhatsNew?: () => void
}

function displayVersion(version: string): string {
  return version === 'dev' || version.startsWith('v') ? version : `v${version}`
}

export function RadarVersionLine({
  version,
  manager,
  managerLoading = false,
  onNavigateToHelmRelease,
  onNavigateToGitOps,
  showUpgrade: upgradeAllowed = true,
  onShowWhatsNew,
}: RadarVersionLineProps) {
  const latestVersion = version.latestVersion
  const updateStatus = getVersionUpdateStatus(version.currentVersion, latestVersion)
  const showUpgrade = upgradeAllowed && version.updateAvailable && !!latestVersion && updateStatus.tier !== 'none'
  const whatsNew = onShowWhatsNew && (
    <span className="inline-flex items-center gap-1">
      <span aria-hidden>·</span>
      <button
        type="button"
        onClick={onShowWhatsNew}
        className="inline-flex items-center gap-1 text-accent-text hover:text-accent transition-colors"
      >
        <Megaphone className="h-3.5 w-3.5 shrink-0" aria-hidden />
        更新内容
      </button>
    </span>
  )

  if (!showUpgrade) {
    return (
      <span className="inline-flex flex-wrap items-center gap-x-1">
        <span>Radar <span className="font-mono">{displayVersion(version.currentVersion)}</span></span>
        {whatsNew}
      </span>
    )
  }

  const controller = manager?.controllerRef
  const gitOpsPath = controller
    ? gitOpsRouteForResource({
        apiVersion: controller.group ? `${controller.group}/v1` : undefined,
        kind: controller.kind,
        metadata: { namespace: controller.namespace, name: controller.name },
      })
    : null

  let detail = managerLoading
    ? '正在检查此安装的管理方式。'
    : '无法确认安装管理器。请打开集群内升级说明。'
  let onClick: (() => void) | undefined
  const actionClassName = `inline-flex items-center gap-1 transition-colors ${upgradeActionClassName(updateStatus.tier)}`

  if (manager?.ownership === 'helm' && manager.namespace && manager.release) {
    if (onNavigateToHelmRelease) {
      detail = `由 Helm 发布 ${manager.namespace}/${manager.release} 管理。打开该发布进行升级。`
      onClick = () => onNavigateToHelmRelease(manager.namespace!, manager.release!)
    } else {
      detail = `由 Helm 发布 ${manager.namespace}/${manager.release} 管理。打开集群内升级说明。`
    }
  } else if (controller && gitOpsPath) {
    const objectName = `${controller.namespace ? `${controller.namespace}/` : ''}${controller.name}`
    if (onNavigateToGitOps) {
      detail = `由 ${controller.kind} ${objectName} 管理。打开它并通过 GitOps 升级。`
      onClick = () => onNavigateToGitOps(gitOpsPath)
    } else {
      detail = `由 ${controller.kind} ${objectName} 管理。打开集群内升级说明并通过 GitOps 应用更改。`
    }
  } else if (manager?.ownership === 'gitops') {
    detail = manager.controller
      ? `此安装似乎由 GitOps（${manager.controller}）管理。打开升级说明，并通过其真实来源应用更改。`
      : '此安装似乎由 GitOps 管理。打开升级说明，并通过其真实来源应用更改。'
  }

  const ageDetail = updateAgeDetail(updateStatus)
  const accessibleLabel = `可用版本 ${displayVersion(latestVersion)}${ageDetail ? `。${ageDetail}` : ''} — ${detail}`
  const action = managerLoading ? (
    <span className={actionClassName}>
      <UpgradeLabel version={latestVersion} />
      <span className="sr-only">{detail}</span>
    </span>
  ) : onClick ? (
    <button
      type="button"
      className={actionClassName}
      onClick={onClick}
      aria-label={accessibleLabel}
    >
      <UpgradeLabel version={latestVersion} />
    </button>
  ) : (
    <a
      href={IN_CLUSTER_UPGRADE_URL}
      target="_blank"
      rel="noreferrer"
      className={actionClassName}
      aria-label={accessibleLabel}
    >
      <UpgradeLabel version={latestVersion} />
    </a>
  )

  return (
    <span className="inline-flex flex-wrap items-center gap-x-1">
      <span>Radar <span className="font-mono">{displayVersion(version.currentVersion)}</span></span>
      <span className="inline-flex items-center gap-1">
        <span aria-hidden>·</span>
        <Tooltip
          content={accessibleLabel}
          className="!whitespace-normal !max-w-sm"
        >
          {action}
        </Tooltip>
      </span>
      {whatsNew}
    </span>
  )
}

function upgradeActionClassName(tier: VersionUpdateTier): string {
  if (tier === 'patch') return 'text-accent-text hover:text-accent'
  if (tier === 'minor') return 'font-medium text-accent hover:text-accent-light'
  return 'font-medium text-warning-text hover:opacity-80'
}

function updateAgeDetail(status: ReturnType<typeof getVersionUpdateStatus>): string | undefined {
  if (status.majorVersionBehind) return '有可用的 Radar 主版本升级。'
  if (status.tier !== 'stale' || !status.minorVersionsBehind) return undefined
  return `此安装落后 ${status.minorVersionsBehind} 个次版本。`
}

function UpgradeLabel({ version }: { version: string }) {
  return (
    <>
      <ArrowUpCircle className="h-3.5 w-3.5 shrink-0" aria-hidden />
      <span>可用版本 <span className="font-mono">{displayVersion(version)}</span></span>
    </>
  )
}
