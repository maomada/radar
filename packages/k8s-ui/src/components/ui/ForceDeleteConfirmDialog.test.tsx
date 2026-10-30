// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ForceDeleteConfirmDialog } from './ForceDeleteConfirmDialog'

let root: Root
const base = { open: true, onClose: vi.fn(), onConfirm: vi.fn(), resourceName: 'web', resourceKind: 'Deployment', namespaceName: 'demo', isLoading: false }
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})
afterEach(async () => {
  await act(async () => root.unmount())
  document.body.replaceChildren()
  vi.clearAllMocks()
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
})

describe('Deletion preview states', () => {
  it('states the namespace-wide deletion consequence without relying on topology', async () => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} resourceKind="Namespace" resourceName="demo" namespaceName="" cascadeRootResolved={false} />))
    expect(document.body.textContent).toContain('永久删除命名空间“demo”及其中的所有资源')
    expect(document.body.textContent).not.toContain("不在 Radar 的拓扑中")
  })
  it('shows one advisory statement for a resolved preview', async () => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved cascadeDependents={[{ kind: 'Pod', namespace: 'demo', name: 'web-pod' }]} />))
    const toggle = [...document.querySelectorAll('button')].find(b => b.textContent?.includes('也可能删除'))!
    await act(async () => toggle.click())
    expect(document.body.textContent).toContain('web-pod')
    expect(document.body.textContent).toContain('Kubernetes 只会删除将此资源列为所有者的对象')
    expect(document.body.textContent).not.toContain('垃圾回收预计将删除')
  })
  it.each([[], undefined])('omits the advisory statement for an empty resolved preview (%j)', async (dependents) => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved cascadeDependents={dependents} />))
    expect(document.body.textContent).not.toContain('可能会影响')
    expect(document.body.textContent).not.toContain('无法验证 Kubernetes 还会删除哪些从属资源')
    expect(document.body.textContent).not.toContain('删除预览不可用')
    expect(document.body.textContent).not.toContain('垃圾回收预计将删除')
  })
  it('distinguishes an observed unresolved root from a fetch failure', async () => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved={false} />))
    expect(document.body.textContent).toContain('Radar 无法验证 Kubernetes 还会删除哪些从属资源。')
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved={false} cascadePreviewFailed cascadeDependents={[{ kind: 'Pod', namespace: 'demo', name: 'stale' }]} />))
    expect(document.body.textContent).toContain('删除预览不可用')
    expect(document.body.textContent).toContain('还可能有其他资源被删除。')
    expect(document.body.textContent).not.toContain('此资源不在 Radar 的拓扑中')
    expect(document.body.textContent).not.toContain('Other resources may be affected')
    expect(document.body.textContent).not.toContain('尚未验证从属资源')
    expect(document.body.textContent).not.toContain('可能会影响')
    const confirm = [...document.querySelectorAll('button')].find(b => b.textContent === '删除')!
    expect(confirm.disabled).toBe(false)
    await act(async () => confirm.click())
    expect(base.onConfirm).toHaveBeenCalledWith(false)
  })
  it('shows loading without presenting stale failure or success as a result', async () => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeLoading cascadePreviewFailed cascadeRootResolved />))
    expect(document.body.textContent).toContain('正在检查从属资源')
    const confirm = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent === '删除')!
    expect(confirm.disabled).toBe(true)
    expect(document.body.textContent).not.toContain('删除预览不可用')
    expect(document.body.textContent).not.toContain('无法验证 Kubernetes 还会删除哪些从属资源')
  })
})

 it('separates observed collection, uncertainty and partial coverage', async () => {
  await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved cascadeDependents={[{kind:'Pod', namespace:'demo', name:'certain'}, {kind:'Pod', namespace:'demo', name:'certain-2'}]} cascadeDetail={{basis:'ownerReferences', possibleDependents:[{kind:'ConfigMap', namespace:'demo', name:'possible'}], coverage:{complete:false,narrowed:true}}} />))
  expect(document.body.textContent).toContain('垃圾回收预计将删除 2 个从属资源')
  expect(document.body.textContent).toContain('也可能删除 1 个资源')
  expect(document.body.textContent).toContain('此预览不完整')
 })
 it('does not promise force deletion can undo controller cleanup', async () => {
  await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeDetail={{controllerTeardown:{controller:'Argo CD', action:'prune', destination:'remote'}}} />))
  const force = document.querySelector('input[type="checkbox"]') as HTMLInputElement
  await act(async () => force.click())
  expect(document.body.textContent).toContain('可能跳过其剩余清理')
  expect(document.body.textContent).toContain('无法撤销已发生的操作')
  expect(document.body.textContent).not.toContain('资源会保留')
 })

 it('changes the pending-orphan warning when force is selected', async () => {
  await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved cascadeDependents={[{kind:'Pod', namespace:'demo', name:'child'}]} cascadeDetail={{basis:'ownerReferences', propagation:'Orphan'}} />))
  expect(document.body.textContent).toContain('所有者引用的从属资源将被孤立')
  expect(document.body.textContent).not.toContain('垃圾回收预计将删除')
  await act(async () => (document.querySelector('input[type="checkbox"]') as HTMLInputElement).click())
  expect(document.body.textContent).not.toContain('所有者引用的从属资源将被孤立')
  expect(document.body.textContent).toContain('强制删除会移除 orphan finalizer')
  expect(document.body.textContent).toContain('垃圾回收预计将删除')
 })

describe('paused Crossplane deletion confirmation', () => {
  it('keeps context and permissions while changing only the explicit force selection', async () => {
    const resourceData = { metadata: { name: 'bucket', annotations: { 'crossplane.io/paused': 'true' }, finalizers: ['finalizer.managedresource.crossplane.io'] }, spec: { providerConfigRef: { name: 'default' } } }
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} resourceData={resourceData} />))
    expect(document.body.textContent).toContain('暂停可能使删除停留在 Terminating 状态')
    const confirm = [...document.querySelectorAll('button')].find(b => b.textContent === '删除')!
    expect(confirm.disabled).toBe(false)
    await act(async () => confirm.click())
    expect(base.onConfirm).toHaveBeenLastCalledWith(false)
    expect(document.body.textContent).toContain('Kubernetes 可以接受删除请求')
    const force = document.querySelector('input[type="checkbox"]') as HTMLInputElement
    await act(async () => force.click())
    expect(document.body.textContent).toContain('强制删除会绕过控制器清理')
    const forceConfirm = [...document.querySelectorAll('button')].find(b => b.textContent === '强制删除')!
    await act(async () => forceConfirm.click())
    expect(base.onConfirm).toHaveBeenLastCalledWith(true)
    expect(resourceData.metadata.finalizers).toEqual(['finalizer.managedresource.crossplane.io'])
    expect(resourceData.metadata.annotations['crossplane.io/paused']).toBe('true')
  })
})
