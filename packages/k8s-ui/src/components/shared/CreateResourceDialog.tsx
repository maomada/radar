import { useCallback, useEffect, useRef, useState, type DragEvent as ReactDragEvent, useId } from 'react'
import { AlertTriangle, Check, Loader2, Upload, X } from 'lucide-react'
import { Collapse, CollapseChevron, useDisclosure } from '../ui/Collapse'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import { DialogPortal } from '../ui/DialogPortal'
import { YamlEditor, type YamlSchemaLoader } from '../ui/YamlEditor'
import {
  reviewedResourceVersionsForPreview,
  YamlReview,
  type YamlPreviewResult,
} from '../ui/YamlReview'
import { useDebouncedValue } from '../../hooks/useDebouncedValue'
import { CrossplaneYamlApplyNotice } from '../ui/CrossplaneOperationNotice'
import { Tooltip } from '../ui/Tooltip'
import { formatApplyError } from '../../utils/k8s-errors'
import {
  describeFileDrag,
  needsReplaceConfirmation,
  readYamlFile,
  YAML_FILE_ACCEPT,
  type FileDragState,
} from '../../utils/yaml-file-import'

// A frame this close to the last one means the main thread is keeping up; a
// blocked editor produces gaps far larger.
const responsiveFrameMs = 100

export interface ApplyResult {
  name: string
  namespace: string
  kind: string
  apiVersion?: string
  uid?: string
  created: boolean
}

export interface CreateResourceDialogProps {
  open: boolean
  onClose: () => void
  /** Return the current draft to a parent flow without closing that flow. */
  onBack?: (yaml: string) => void
  backLabel?: string
  initialYaml?: string
  title?: string
  onApply: (params: {
    yaml: string
    mode: 'apply' | 'create'
    dryRun: boolean
    force: boolean
    reviewedResourceVersions?: Record<number, string>
    reviewedContext?: string
  }) => Promise<ApplyResult[]>
  isApplying: boolean
  onPreview?: (params: { yaml: string; mode: 'apply' | 'create'; force: boolean }) => Promise<{
    documents: YamlPreviewResult[]
    nonAtomic: boolean
    context?: string
  }>
  isPreviewing?: boolean
  previewError?: string | null
  schemaLoader?: YamlSchemaLoader
  /** Receives the first result and the exact submitted manifest, after a successful write. */
  onCreated?: (result: ApplyResult, submittedYaml: string) => void
  /** Start in strict create mode (e.g. a prefilled manifest that must not update an existing object). */
  initialMode?: 'apply' | 'create'
  /** Stay in `initialMode` with the Apply/Create choice and Force hidden, even after a partial create (e.g. a new object that must only be created). */
  lockMode?: boolean
}

export function CreateResourceDialog({
  open,
  onClose,
  onBack,
  backLabel = '返回',
  initialYaml = '',
  title,
  onApply,
  isApplying,
  onPreview,
  isPreviewing = false,
  previewError,
  schemaLoader,
  onCreated,
  initialMode = 'apply',
  lockMode = false,
}: CreateResourceDialogProps) {
  const titleId = useId()
  const [yaml, setYaml] = useState(initialYaml)
  const pauseContextYaml = useDebouncedValue(onPreview ? '' : yaml, 200)
  const [mode, setMode] = useState<'apply' | 'create'>(initialMode)
  const [dryRun, setDryRun] = useState(false)
  const [force, setForce] = useState(false)
  const [yamlValid, setYamlValid] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)
  const [partialCreates, setPartialCreates] = useState<ApplyResult[]>([])
  const [preview, setPreview] = useState<{
    yaml: string
    mode: 'apply' | 'create'
    force: boolean
    documents: YamlPreviewResult[]
    nonAtomic: boolean
    context?: string
  } | null>(null)
  const closeTimer = useRef<number | undefined>(undefined)
  const [drag, setDrag] = useState<FileDragState>('none')
  const [pendingImport, setPendingImport] = useState<{ fileName: string; yaml: string } | null>(null)
  const [importing, setImporting] = useState<string | null>(null)
  // An import is several awaits and several frames long. Anything that changes
  // what the result should land on — a newer import, a close, a reopen — bumps
  // the generation, and every deferred step checks it before touching state.
  const importGeneration = useRef(0)
  const scheduledFrames = useRef(new Set<number>())
  // The buffer as it stands now, not as it stood when a read began.
  const yamlRef = useRef(yaml)
  const fileInputRef = useRef<HTMLInputElement>(null)
  // dragenter/dragleave also fire for the editor's own descendants, so the
  // overlay tracks depth rather than the first dragleave it sees.
  const dragDepth = useRef(0)

  useEffect(() => {
    yamlRef.current = yaml
  }, [yaml])

  const retireDeferredImports = useCallback(() => {
    importGeneration.current += 1
    for (const id of scheduledFrames.current) cancelAnimationFrame(id)
    scheduledFrames.current.clear()
  }, [])

  const cancelScheduledClose = useCallback(() => {
    window.clearTimeout(closeTimer.current)
    closeTimer.current = undefined
  }, [])
  useEffect(() => () => { retireDeferredImports(); cancelScheduledClose() }, [retireDeferredImports, cancelScheduledClose])

  const scheduleFrame = useCallback((generation: number, run: () => void) => {
    const id = requestAnimationFrame(() => {
      scheduledFrames.current.delete(id)
      if (generation !== importGeneration.current) return
      run()
    })
    scheduledFrames.current.add(id)
  }, [])

  useEffect(() => {
    if (!open) return
    cancelScheduledClose()
    setYaml(initialYaml)
    setMode(initialMode)
    setDryRun(false)
    setForce(false)
    setYamlValid(true)
    setError(null)
    setPartialCreates([])
    setSuccess(null)
    setPreview(null)
    setDrag('none')
    setPendingImport(null)
    setImporting(null)
    dragDepth.current = 0
    retireDeferredImports()
  }, [open, initialYaml, initialMode, retireDeferredImports, cancelScheduledClose])

  const pending = isApplying || isPreviewing
  const closeNow = useCallback(() => {
    cancelScheduledClose()
    retireDeferredImports()
    setError(null)
    setPartialCreates([])
    setSuccess(null)
    setPreview(null)
    setPendingImport(null)
    setImporting(null)
    setYaml('')
    onClose()
  }, [onClose, retireDeferredImports, cancelScheduledClose])
  const handleClose = useCallback(() => {
    if (!pending) closeNow()
  }, [closeNow, pending])

  const handleValidate = useCallback((_isValid: boolean, errors: string[]) => {
    setYamlValid(errors.length === 0)
  }, [])

  const finishApply = useCallback(
    (results: ApplyResult[], appliedMode: 'apply' | 'create', wasDryRun: boolean, submittedYaml: string) => {
      const action = appliedMode === 'create' ? '已创建' : '已应用'
      const dryRunLabel = wasDryRun ? ' (dry run)' : ''
      if (results.length === 1) {
        const result = results[0]
        setSuccess(
          `${action} ${result.kind} ${result.namespace ? `${result.namespace}/` : ''}${result.name}${dryRunLabel}`,
        )
      } else {
        setSuccess(`${action} ${results.length} resources${dryRunLabel}`)
      }
      if (wasDryRun) return
      setPartialCreates([])
      if (onCreated && results.length > 0) {
        closeNow()
        onCreated(results[0], submittedYaml)
      } else {
        closeTimer.current = window.setTimeout(closeNow, 1200)
      }
    },
    [closeNow, onCreated],
  )

  // Handing a document to the editor parses the whole buffer and blocks the
  // main thread, so the status has to be on screen before that starts. Two
  // frames: the first commits it, the second lets the browser paint it. A
  // spinner would only freeze mid-turn, so this says what is happening instead
  // of pretending to animate.
  const loadIntoEditor = useCallback(
    (fileName: string, content: string, generation: number) => {
      setImporting(fileName)
      scheduleFrame(generation, () => {
        scheduleFrame(generation, () => {
          setYaml(content)
          // Clearing it in this same callback would batch it into the very
          // render that blocks, retiring the status while the editor is still
          // unusable. Wait for a frame to arrive on time instead: that is the
          // editor answering again, which is what the status was promising.
          let previous = performance.now()
          const clearWhenResponsive = () => {
            const now = performance.now()
            if (now - previous < responsiveFrameMs) {
              setImporting(null)
              return
            }
            previous = now
            scheduleFrame(generation, clearWhenResponsive)
          }
          scheduleFrame(generation, clearWhenResponsive)
        })
      })
    },
    [scheduleFrame],
  )

  const loadFiles = useCallback(
    async (files: File[]) => {
      const generation = ++importGeneration.current
      const result = await readYamlFile(files)
      // A newer import, a close or a reopen happened while this one was
      // reading; its result describes an editor that has moved on.
      if (generation !== importGeneration.current) return
      // This import owns the status from here. Taking the generation retired
      // the frames that would have cleared a previous import's overlay, so
      // every path out of this function has to leave it in a sane state — the
      // ones that return early below would otherwise strand it on screen.
      setImporting(null)
      if (!result.ok) {
        setSuccess(null)
        setError(result.message)
        return
      }
      setError(null)
      setSuccess(null)
      if (needsReplaceConfirmation(yamlRef.current, initialYaml)) {
        setPendingImport({ fileName: result.fileName, yaml: result.yaml })
        return
      }
      loadIntoEditor(result.fileName, result.yaml, generation)
    },
    [initialYaml, loadIntoEditor],
  )

  const handleDragEnter = useCallback((event: ReactDragEvent) => {
    const state = describeFileDrag(event.dataTransfer)
    if (state === 'none') return
    event.preventDefault()
    dragDepth.current += 1
    setDrag(state)
  }, [])

  const handleDragOver = useCallback((event: ReactDragEvent) => {
    if (describeFileDrag(event.dataTransfer) === 'none') return
    // Without this the browser navigates away to the dropped file.
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
  }, [])

  const handleDragLeave = useCallback((event: ReactDragEvent) => {
    if (describeFileDrag(event.dataTransfer) === 'none') return
    dragDepth.current = Math.max(0, dragDepth.current - 1)
    if (dragDepth.current === 0) setDrag('none')
  }, [])

  const handleDrop = useCallback(
    (event: ReactDragEvent) => {
      if (describeFileDrag(event.dataTransfer) === 'none') return
      event.preventDefault()
      dragDepth.current = 0
      setDrag('none')
      // The DataTransfer is neutered once this handler returns, so take the
      // files off it before anything awaits.
      void loadFiles(Array.from(event.dataTransfer.files))
    },
    [loadFiles],
  )

  const recoverPartialCreate = useCallback((caught: unknown, submittedYaml: string, appliedMode: 'apply' | 'create', wasDryRun: boolean) => {
    const appliedResults = caught instanceof Error && 'appliedResults' in caught && Array.isArray(caught.appliedResults) ? caught.appliedResults : []
    const createdResults = appliedMode === 'create' ? appliedResults : appliedResults.filter(result => result.created)
    if (wasDryRun || createdResults.length === 0) return false
    setYaml(submittedYaml)
    if (!lockMode && onPreview) setMode('apply')
    setPreview(null)
    setPartialCreates(previous => [...previous, ...createdResults])
    return true
  }, [lockMode, onPreview])

  const handleSubmit = useCallback(async () => {
    if (!yaml.trim()) {
      setError('必须填写 YAML 内容')
      return
    }
    setError(null)
    setSuccess(null)
    try {
      if (onPreview) {
        const reviewedForce = mode === 'apply' && force
        const result = await onPreview({ yaml, mode, force: reviewedForce })
        setPreview({
          yaml,
          mode,
          force: reviewedForce,
          documents: result.documents,
          nonAtomic: result.nonAtomic,
          context: result.context,
        })
        return
      }
      const results = await onApply({
        yaml,
        mode,
        dryRun,
        force: mode === 'apply' && force,
      })
      finishApply(results, mode, dryRun, yaml)
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : '未知错误')
      if (!onPreview) recoverPartialCreate(caught, yaml, mode, dryRun)
    }
  }, [yaml, mode, dryRun, force, onApply, onPreview, finishApply, recoverPartialCreate])

  const handleApplyReviewed = useCallback(async () => {
    if (!preview) return
    setError(null)
    try {
      const reviewedResourceVersions = reviewedResourceVersionsForPreview(preview.documents)
      const results = await onApply({
        yaml: preview.yaml,
        mode: preview.mode,
        dryRun: false,
        force: preview.force,
        reviewedResourceVersions,
        reviewedContext: preview.context,
      })
      finishApply(results, preview.mode, false, preview.yaml)
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : '未知错误'
      setError(message)
      if (recoverPartialCreate(caught, preview.yaml, preview.mode, false)) return
      if (onPreview) {
        try {
          const refreshed = await onPreview({
            yaml: preview.yaml,
            mode: preview.mode,
            force: preview.force,
          })
          setPreview({
            ...preview,
            documents: refreshed.documents,
            nonAtomic: refreshed.nonAtomic,
            context: refreshed.context,
          })
        } catch {
          // Keep the last review visible when refresh is unavailable.
        }
      }
    }
  }, [preview, onApply, onPreview, finishApply, recoverPartialCreate])

  const dialogTitle = title || '创建资源'
  const showModeControls = !lockMode
  const submitLabel = onPreview ? '审阅' : mode === 'create' ? '创建' : '应用'

  return (
    <DialogPortal
      ariaLabelledBy={titleId}
      open={open}
      onClose={handleClose}
      closable={!pending}
      className={
        preview
          ? 'w-[min(1200px,calc(100vw-2rem))] h-[min(860px,calc(100vh-2rem))] flex flex-col'
          : 'w-[700px] max-h-[85vh] flex flex-col'
      }
    >
      {preview ? (
        <YamlReview
          titleId={titleId}
          submittedYaml={preview.yaml}
          mode={preview.mode}
          documents={preview.documents}
          nonAtomic={preview.nonAtomic}
          force={preview.force}
          isApplying={isApplying}
          applyError={error}
          applyLabel={
            preview.mode === 'create' ? '创建已审阅的资源' : '应用已审阅的更改'
          }
          onClose={handleClose}
          onBack={() => {
            setError(null)
            setPreview(null)
          }}
          onApply={handleApplyReviewed}
        />
      ) : (
        <>
          <div className="flex shrink-0 items-center justify-between border-b border-theme-border px-5 py-3.5">
            <h2 id={titleId} className="text-sm font-semibold text-theme-text-primary">{dialogTitle}</h2>
            <Tooltip content="关闭">
              <button
                type="button"
                onClick={handleClose}
                aria-label="关闭"
                className="rounded p-1 text-theme-text-secondary transition-colors hover:bg-theme-hover"
              >
                <X className="h-4 w-4" />
              </button>
            </Tooltip>
          </div>

          <div className="flex min-h-0 flex-1 flex-col px-5 py-3">
            <div className="mb-2 flex shrink-0 items-center gap-2">
              <button
                type="button"
                onClick={() => fileInputRef.current?.click()}
                className="flex items-center gap-1.5 rounded-md border border-theme-border px-2.5 py-1 text-xs font-medium text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
              >
                <Upload className="h-3.5 w-3.5" />
                导入 YAML 文件
              </button>
              <span className="text-xs text-theme-text-tertiary">
                或将 YAML 文件拖放到编辑器中
              </span>
              <input
                ref={fileInputRef}
                type="file"
                accept={YAML_FILE_ACCEPT}
                className="hidden"
                onChange={(event) => {
                  const picked = Array.from(event.target.files ?? [])
                  // Clearing the input empties its FileList, so read it first.
                  event.target.value = ''
                  void loadFiles(picked)
                }}
              />
            </div>

            <div
              className="relative h-[400px] min-h-0"
              onDragEnter={handleDragEnter}
              onDragOver={handleDragOver}
              onDragLeave={handleDragLeave}
              onDrop={handleDrop}
            >
              <YamlEditor
                value={yaml}
                onChange={setYaml}
                height="100%"
                onValidate={handleValidate}
                schemaLoader={schemaLoader}
              />
              {importing && (
                <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-md border border-theme-border bg-theme-base/85">
                  <span className="text-sm font-medium text-theme-text-primary">
                    正在加载 {importing}…
                  </span>
                </div>
              )}
              {drag !== 'none' && (
                <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-md border-2 border-dashed border-accent bg-theme-base/85">
                  <span className="flex items-center gap-2 text-sm font-medium text-theme-text-primary">
                    <Upload className="h-4 w-4" />
                    {drag === 'multiple-files'
                      ? '拖放单个 YAML 文件'
                      : '拖放 YAML 文件以加载'}
                  </span>
                </div>
              )}
            </div>
          </div>

          {!onPreview && <div className="px-4"><CrossplaneYamlApplyNotice yaml={pauseContextYaml} baselineUnavailable={mode === 'apply'} /></div>}
          {(error || previewError || partialCreates.length > 0) && (
            <ApplyErrorBanner error={error || previewError || '上次尝试停止前已创建部分资源。'} partialCreates={partialCreates} strictCreate={lockMode && mode === 'create'} hasPreview={Boolean(onPreview)} />
          )}
          {success && (
            <div className="mx-5 mb-2 flex items-center gap-2 rounded-md border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-600 dark:text-emerald-400">
              <Check className="h-3.5 w-3.5 shrink-0" />
              <span>{success}</span>
            </div>
          )}

          <div className="flex shrink-0 items-center justify-between border-t border-theme-border px-5 py-3">
            <div className="flex items-center gap-3">
              {showModeControls && (
                <Tooltip
                  content="应用：创建或更新（幂等）。创建：资源已存在时失败。"
                  position="bottom"
                >
                  <div
                    className="flex items-center rounded-md border border-theme-border bg-theme-base p-0.5"
                    role="radiogroup"
                    aria-label="应用模式"
                  >
                    {(['apply', 'create'] as const).map((option) => (
                      <button
                        type="button"
                        key={option}
                        onClick={() => setMode(option)}
                        role="radio"
                        aria-checked={mode === option}
                        className={`rounded px-2.5 py-1 text-xs font-medium capitalize transition-colors ${
                          mode === option
                            ? 'bg-theme-elevated text-theme-text-primary shadow-theme-sm'
                            : 'text-theme-text-tertiary hover:text-theme-text-secondary'
                        }`}
                      >
                        {option === 'apply' ? '应用' : '创建'}
                      </button>
                    ))}
                  </div>
                </Tooltip>
              )}

              {!onPreview && (
                <Tooltip
                  content="对照集群验证，但不持久化更改"
                  position="bottom"
                >
                  <label className="flex cursor-pointer items-center gap-1.5 text-xs text-theme-text-secondary">
                    <input
                      type="checkbox"
                      checked={dryRun}
                      onChange={(event) => setDryRun(event.target.checked)}
                      className="h-3.5 w-3.5 rounded border-theme-border bg-theme-base"
                    />
                    试运行
                  </label>
                </Tooltip>
              )}

              {showModeControls && (
                <Tooltip
                  content="覆盖字段所有权冲突。活跃控制器可能会将这些字段协调回原值。"
                  position="bottom"
                >
                  <label
                    className={`flex items-center gap-1.5 text-xs ${mode === 'apply' ? 'cursor-pointer text-theme-text-secondary' : 'cursor-not-allowed text-theme-text-tertiary'}`}
                  >
                    <input
                      type="checkbox"
                      checked={mode === 'apply' && force}
                      disabled={mode !== 'apply'}
                      onChange={(event) => setForce(event.target.checked)}
                      className="h-3.5 w-3.5 rounded border-theme-border bg-theme-base"
                    />
                    强制应用
                  </label>
                </Tooltip>
              )}
            </div>

            <div className="flex items-center gap-2">
              {onBack && (
                <button
                  type="button"
                  onClick={() => {
                    cancelScheduledClose()
                    retireDeferredImports()
                    onBack(yamlRef.current)
                  }}
                  disabled={pending || importing !== null}
                  className="btn-secondary rounded-lg px-3 py-1.5 text-xs"
                >
                  {backLabel}
                </button>
              )}
              <button
                type="button"
                onClick={handleClose}
                className="rounded-lg px-3 py-1.5 text-xs text-theme-text-secondary transition-colors hover:bg-theme-hover"
              >
                取消
              </button>
              <button
                type="button"
                onClick={handleSubmit}
                disabled={pending || !yaml.trim() || !yamlValid}
                className="btn-brand flex items-center gap-1.5 rounded-lg px-4 py-1.5 text-xs font-medium disabled:cursor-not-allowed"
              >
                {pending && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                {isPreviewing ? '正在预览…' : isApplying ? `${submitLabel}…` : submitLabel}
              </button>
            </div>
          </div>
        </>
      )}

      <ConfirmDialog
        open={pendingImport !== null}
        onClose={() => setPendingImport(null)}
        onConfirm={() => {
          // Confirming is its own decision about the editor as it stands now,
          // so it loads under a fresh generation rather than the one the read
          // started under, which anything since may have retired.
          if (pendingImport) {
            const generation = ++importGeneration.current
            loadIntoEditor(pendingImport.fileName, pendingImport.yaml, generation)
          }
          setPendingImport(null)
        }}
        variant="warning"
        showWarning={false}
        title="替换编辑器内容？"
        message={`加载 ${pendingImport?.fileName ?? '此文件'} 会丢弃编辑器中当前的 YAML。`}
        confirmLabel="替换"
      />
    </DialogPortal>
  )
}

function ApplyErrorBanner({ error, partialCreates, strictCreate, hasPreview }: { error: string; partialCreates: ApplyResult[]; strictCreate: boolean; hasPreview: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const { panelId, buttonProps } = useDisclosure(expanded)
  const parsed = formatApplyError(error, { allowApply: !strictCreate && partialCreates.length === 0 })
  const hasFriendly = Boolean(parsed.suggestion)
  const recoveringWithReview = partialCreates.length > 0 && !strictCreate && hasPreview
  return (
    <div className="mx-5 mb-2 max-h-[30vh] shrink-0 overflow-y-auto rounded-md border border-red-500/30 bg-red-500/10 text-xs">
      <div className="flex items-start gap-2 px-3 py-2 text-red-600 dark:text-red-400">
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <div className="min-w-0 flex-1">
          <span className="font-medium">{parsed.summary}</span>
          {partialCreates.length > 0 && <p className="mt-1">
            停止前已创建：{partialCreates.map(result => `${result.kind} ${result.namespace ? `${result.namespace}/` : ''}${result.name}`).join('、')}。
            {' '}{recoveringWithReview ? 'Radar 已切换到“应用”模式，以便你审阅并根据当前集群状态继续。' : `${strictCreate ? '此对话框仅用于创建。' : ''}请从清单中移除已创建的对象，然后重试创建。`}
          </p>}
          {parsed.suggestion && !recoveringWithReview && (
            <p className="mt-1 text-red-500/80 dark:text-red-400/80">{parsed.suggestion}</p>
          )}
        </div>
      </div>
      {hasFriendly && (
        <button
          {...buttonProps}
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="flex items-center gap-1 px-3 pb-2 text-red-500/60 hover:text-red-500/80 dark:text-red-400/60 dark:hover:text-red-400/80"
        >
          <CollapseChevron open={expanded} inheritColor className="h-3 w-3" />
          Details
        </button>
      )}
      <Collapse open={expanded && hasFriendly} id={panelId}>
        <div className="break-all px-3 pb-2 font-mono leading-relaxed text-red-500/60 dark:text-red-400/60">
          {parsed.raw}
        </div>
      </Collapse>
    </div>
  )
}
