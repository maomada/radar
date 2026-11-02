import { ShieldCheck } from 'lucide-react'

export function PrivacySection({ active: _active }: { active: boolean }) {
  return (
    <section className="space-y-3 text-sm text-theme-text-secondary">
      <h3 className="flex items-center gap-2 font-medium text-theme-text-primary">
        <ShieldCheck className="h-4 w-4 text-accent" />隐私与数据
      </h3>
      <p>KubeAtlas 不收集或发送匿名使用情况统计，旧的遥测授权和环境变量不会启用上报。</p>
      <p>集群数据由当前 KubeAtlas 实例读取和处理。独立门户仅接收你主动配置的实例注册与心跳信息，不接收集群资源内容。</p>
      <p>你主动配置的指标、GitOps、AI 等外部集成仍按各自配置访问；这里的遥测关闭不代表所有网络请求均被禁止。</p>
    </section>
  )
}
