import { useId, useLayoutEffect, useRef, useState } from 'react'
import type React from 'react'
import { GripVertical, X } from 'lucide-react'
import { clsx } from 'clsx'
import { Tooltip } from '../ui/Tooltip'
import { isMac } from '../../utils/platform'
import { moveColumn, pinnedColumnCount } from './column-order'
import type { Column } from './ResourcesView'

const COLUMN_DISPLAY_LABELS: Record<string, string> = {
  Name: '名称',
  Namespace: '命名空间',
  Containers: '容器',
  Status: '状态',
  Memory: '内存',
  Restarts: '重启次数',
  Node: '节点',
  Age: '运行时长',
  Kind: '类型',
  Type: '类型',
  Ready: '就绪',
  Desired: '期望副本数',
  Available: '可用副本数',
  Created: '创建时间',
  Reason: '原因',
  Labels: '标签',
  Images: '镜像',
  Owner: '所有者',
  'API Version': 'API 版本',
}

export function columnDisplayLabel(label: string): string {
  return COLUMN_DISPLAY_LABELS[label] ?? label
}

interface ColumnPickerListProps {
  /** Every offered column, in display order. */
  columns: Column[]
  visibleColumns: Set<string>
  customColumnKeys: Set<string>
  onToggle: (key: string) => void
  onRemoveCustom: (key: string) => void
  /** Receives the movable column keys in their new order. */
  onReorder: (order: string[]) => void
}

/** The Columns picker's rows: show/hide, remove a custom column, and reorder by
 *  dragging the grip or with Alt/Option+Up/Down on the row's checkbox. */
export function ColumnPickerList({
  columns,
  visibleColumns,
  customColumnKeys,
  onToggle,
  onRemoveCustom,
  onReorder,
}: ColumnPickerListProps) {
  const pinned = pinnedColumnCount(columns)
  const [dragKey, setDragKey] = useState<string | null>(null)
  // Insertion point among the movable rows: 0 = before the first one.
  const [dropGap, setDropGap] = useState<number | null>(null)
  const [announcement, setAnnouncement] = useState('')
  const hintId = useId()
  const mac = isMac()

  // React moves a keyed row by re-inserting DOM nodes, and re-inserting the
  // focused checkbox drops focus. Put it back so repeated presses keep working.
  const refocus = useRef<HTMLElement | null>(null)
  useLayoutEffect(() => {
    const el = refocus.current
    if (!el) return
    refocus.current = null
    if (el.isConnected && document.activeElement !== el) el.focus()
  })

  const commitMove = (key: string, toIndex: number) => {
    const next = moveColumn(columns, key, toIndex)
    if (!next) return false
    onReorder(next)
    const col = columns.find(c => c.key === key)
    setAnnouncement(`${columnDisplayLabel(col?.label ?? key)}已移动到第 ${pinned + next.indexOf(key) + 1} 列，共 ${columns.length} 列。`)
    return true
  }

  const handleKeyDown = (e: React.KeyboardEvent, key: string, movableIndex: number) => {
    if (!e.altKey || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown')) return
    e.preventDefault()
    e.stopPropagation()
    const up = e.key === 'ArrowUp'
    refocus.current = e.target instanceof HTMLElement ? e.target : null
    if (!commitMove(key, movableIndex + (up ? -1 : 1))) {
      refocus.current = null
      const label = columnDisplayLabel(columns.find(c => c.key === key)?.label ?? key)
      setAnnouncement(up ? `${label}已位于最顶部。` : `${label}已位于最底部。`)
    }
  }

  // The gap above the row when the pointer is in its top half, else below it.
  const gapAt = (e: React.DragEvent, movableIndex: number) => {
    const rect = e.currentTarget.getBoundingClientRect()
    return e.clientY < rect.top + rect.height / 2 ? movableIndex : movableIndex + 1
  }

  const endDrag = () => {
    setDragKey(null)
    setDropGap(null)
  }

  return (
    <>
      <span id={hintId} className="sr-only">
        {mac ? '按 Option+↑ 或 Option+↓ 移动此列。' : '按 Alt+↑ 或 Alt+↓ 移动此列。'}
      </span>
      <span className="sr-only" role="status" aria-live="polite">{announcement}</span>
      {columns.map((col, i) => {
        const isCustom = customColumnKeys.has(col.key)
        const movableIndex = i - pinned
        const movable = movableIndex >= 0
        const fromIndex = dragKey ? columns.findIndex(c => c.key === dragKey) - pinned : -1
        const showGap = (gap: number) => dropGap === gap && gap !== fromIndex && gap !== fromIndex + 1
        return (
          <div
            key={col.key}
            data-column-row
            className={clsx(
              'group relative flex items-center gap-1.5 pl-1.5 pr-3 py-1.5 hover:bg-theme-elevated',
              dragKey === col.key && 'opacity-50',
            )}
            onKeyDown={movable ? e => handleKeyDown(e, col.key, movableIndex) : undefined}
            onDragOver={movable && dragKey ? e => {
              e.preventDefault()
              e.dataTransfer.dropEffect = 'move'
              setDropGap(gapAt(e, movableIndex))
            } : undefined}
            onDrop={movable && dragKey ? e => {
              e.preventDefault()
              const gap = gapAt(e, movableIndex)
              commitMove(dragKey, gap > fromIndex ? gap - 1 : gap)
              endDrag()
            } : undefined}
          >
            {movable && showGap(movableIndex) && (
              <span aria-hidden className="pointer-events-none absolute inset-x-2 -top-px h-0.5 rounded-full bg-accent" />
            )}
            {movable && showGap(movableIndex + 1) && i === columns.length - 1 && (
              <span aria-hidden className="pointer-events-none absolute inset-x-2 -bottom-px h-0.5 rounded-full bg-accent" />
            )}
            {movable ? (
              <Tooltip content={mac ? '拖动以重新排序，或按 ⌥↑ / ⌥↓' : '拖动以重新排序，或按 Alt+↑ / Alt+↓'} position="left">
                <span
                  aria-hidden
                  draggable
                  onDragStart={e => {
                    e.dataTransfer.effectAllowed = 'move'
                    // Firefox starts a drag only when data is set.
                    e.dataTransfer.setData('text/plain', col.key)
                    const row = e.currentTarget.closest('[data-column-row]')
                    if (row instanceof HTMLElement) e.dataTransfer.setDragImage(row, 12, row.offsetHeight / 2)
                    setDragKey(col.key)
                  }}
                  onDragEnd={endDrag}
                  className="flex shrink-0 cursor-grab items-center text-theme-text-tertiary opacity-60 group-hover:opacity-100 active:cursor-grabbing"
                >
                  <GripVertical className="w-3.5 h-3.5" />
                </span>
              </Tooltip>
            ) : (
              <span aria-hidden className="w-3.5 shrink-0" />
            )}
            <label className="flex items-center gap-2 flex-1 min-w-0 cursor-pointer">
              <input
                type="checkbox"
                checked={visibleColumns.has(col.key)}
                onChange={() => onToggle(col.key)}
                disabled={col.key === 'name'}
                aria-describedby={movable ? hintId : undefined}
                className="rounded border-theme-border"
              />
              <span className={clsx(
                'text-sm truncate',
                col.key === 'name' ? 'text-theme-text-tertiary' : 'text-theme-text-primary'
              )} title={col.tooltip || columnDisplayLabel(col.label)}>
                {columnDisplayLabel(col.label)}
              </span>
            </label>
            {isCustom && (
              <button
                type="button"
                onClick={() => onRemoveCustom(col.key)}
                className="shrink-0 p-0.5 text-theme-text-tertiary hover:text-red-500 opacity-0 group-hover:opacity-100 focus:opacity-100"
                title="移除列"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            )}
          </div>
        )
      })}
    </>
  )
}
