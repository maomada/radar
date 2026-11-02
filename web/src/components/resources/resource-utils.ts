// Re-export all resource utilities from the shared @maomada/k8s-ui package.
export * from '@maomada/k8s-ui/components/resources/resource-utils'

// formatBytes lives in utils/format but is re-exported here so consumers
// can import it from the same module as the rest of the resource utilities.
export { formatBytes } from '@maomada/k8s-ui/utils/format'
