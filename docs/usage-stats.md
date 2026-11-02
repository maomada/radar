# 使用情况统计

KubeAtlas 不记录或发送匿名使用情况统计。旧的本地授权状态以及
`KUBEATLAS_USAGE_REPORTING` 环境变量都不会启用上报；`postReport` 不会发起
HTTP 请求。

Helm 中 `usageReporting.enabled` 应保持未设置或为 `false`。设为 `true`
时模板会明确失败，而不是悄悄忽略配置或发送数据。

这项限制只针对使用情况统计，不会阻止你主动配置的 Kubernetes、
Prometheus、GitOps、AI 或其他集成按各自配置访问网络。

KubeAtlas 使用新的 `~/.kubeatlas` 本地配置目录，不会自动读取、复制或删除
旧 `~/.radar` 目录中的状态或报告文件。需要保留旧配置时，请先人工审查并
仅迁移明确需要的非敏感设置。
