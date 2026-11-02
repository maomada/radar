import { ConsentCard } from '@maomada/k8s-ui'

const wrap = { width: 480, padding: 8 }
const noop = () => {}

export function Safeguarded() {
  return (
    <div style={wrap}>
      <ConsentCard agentName="Claude Code" agent="claude" profile="safeguarded" onOpenSettings={noop} onApprove={noop} onCancel={noop} />
    </div>
  )
}

export function FullLocalSetup() {
  return (
    <div style={wrap}>
      <ConsentCard agentName="Codex" agent="codex" profile="full-local" onOpenSettings={noop} onApprove={noop} onCancel={noop} />
    </div>
  )
}

export function ApprovalFailed() {
  return (
    <div style={wrap}>
      <ConsentCard
        agentName="Claude Code"
        agent="claude"
        profile="safeguarded"
        error="Consent could not be recorded: the local KubeAtlas settings file is read-only (~/.kubeatlas/settings.json)."
        onOpenSettings={noop}
        onApprove={noop}
        onCancel={noop}
      />
    </div>
  )
}

export function HostCopy() {
  return (
    <div style={wrap}>
      <ConsentCard
        agentName="KubeAtlas Agent"
        profile="safeguarded"
        copy={{
          title: 'Start an investigation with KubeAtlas Agent?',
          body: 'KubeAtlas Agent runs in your organization’s upstream Cloud workspace. It reads this resource’s spec, recent events and pod logs through read-only tools; transcripts are retained for 30 days.',
          bullets: ['Usage counts toward your organization’s monthly investigation quota.', 'Investigations never change your cluster without a separate Apply confirmation.'],
          settingsLabel: null,
          approveLabel: 'Start investigation',
        }}
        onApprove={noop}
        onCancel={noop}
      />
    </div>
  )
}
