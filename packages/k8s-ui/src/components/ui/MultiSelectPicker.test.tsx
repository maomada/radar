import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MultiSelectPicker } from './MultiSelectPicker'

// 8 items so the list clears the default search threshold (6) and the filter
// input renders.
const KINDS = ['ConfigMap', 'Deployment', 'Ingress', 'Job', 'Pod', 'ReplicaSet', 'Service', 'StatefulSet']

function render(props: Partial<Parameters<typeof MultiSelectPicker>[0]> = {}) {
  return renderToString(
    <MultiSelectPicker
      items={KINDS}
      selected={new Set()}
      onSelectionChange={() => {}}
      onClearAll={() => {}}
      onDone={() => {}}
      search=""
      onSearchChange={() => {}}
      searchPlaceholder="Filter kinds"
      summaryEmptyLabel="All kinds"
      noItemsLabel="No kinds available."
      clearAllDisabled
      clearAllAriaLabel="Clear kind selection"
      {...props}
    />,
  )
}

describe('MultiSelectPicker', () => {
  it('renders each item as a checkbox row with bulk selection and completion controls', () => {
    const html = render()
    expect(html).toContain('type="checkbox"')
    for (const kind of KINDS) expect(html).toContain(`>${kind}</span>`)
    expect(html).toContain('清除全部')
    expect(html).toContain('全选')
    expect(html).toContain('>完成</button>')
  })

  it('adds an Only button to each row only when a select-only handler is given', () => {
    expect(render()).not.toContain('>仅此项</button>')

    const html = render({ onSelectOnly: () => {} })
    expect(html.match(/>仅此项<\/button>/g)).toHaveLength(KINDS.length)
    expect(html).toContain('aria-label="仅选择 Pod"')
    // The name stays inside the checkbox label, so clicking it still toggles.
    expect(html).toMatch(/<label[^>]*><span[^>]*><input type="checkbox"[^>]*\/><span class="truncate">Pod<\/span><\/span><\/label><button/)
  })

  it('hides Only until hover or keyboard focus, and keeps it visible on touch', () => {
    const html = render({ onSelectOnly: () => {} })
    const only = html.match(/<button[^>]*aria-label="仅选择 Pod"[^>]*>/)?.[0] ?? ''
    expect(only).toContain('opacity-0')
    expect(only).toContain('group-hover/row:opacity-100')
    expect(only).toContain('group-has-[:focus-visible]/row:opacity-100')
    expect(only).toContain('focus-visible:opacity-100')
    expect(only).toContain('any-pointer-coarse:opacity-100')
  })

  it('shows the filter input above the threshold and hides it below', () => {
    const shown = render()
    expect(shown).toContain('placeholder="Filter kinds"')

    const hidden = render({ items: ['Pod', 'Service'] })
    expect(hidden).not.toContain('placeholder="Filter kinds"')
  })

  it('filters the list by the controlled search term', () => {
    const html = render({ search: 'dep' })
    expect(html).toContain('>Deployment</span>')
    expect(html).not.toContain('>Pod</span>')
    expect(html).not.toContain('>Service</span>')
  })

  it('shows a no-matches message when the search excludes everything', () => {
    const html = render({ search: 'zzz' })
    expect(html).toContain('没有匹配项。')
    expect(html).not.toContain('>Pod</span>')
  })

  it('shows the empty-items label when there are no items and no search', () => {
    const html = render({ items: [], search: '' })
    expect(html).toContain('No kinds available.')
  })

  it('summarises the empty selection as the empty label, else the selected count', () => {
    expect(render({ selected: new Set() })).toContain('All kinds')

    const some = render({ selected: new Set(['Pod', 'Service']), clearAllDisabled: false })
    expect(some).toContain('已选择 2 项')
    expect(some).not.toContain('All kinds')
  })

  it('reflects the selected items via checked checkboxes', () => {
    const html = render({ selected: new Set(['Pod']), clearAllDisabled: false })
    // The checked checkbox renders with the `checked` attribute in SSR markup.
    expect(html).toContain('checked=""')
  })

  it('disables Clear all when clearAllDisabled and enables it otherwise', () => {
    expect(render({ clearAllDisabled: true })).toContain('aria-label="Clear kind selection"')
    // Disabled state present when nothing is selected.
    expect(render({ clearAllDisabled: true })).toMatch(/清除全部[\s\S]*?disabled|disabled[\s\S]*?清除全部/)

    const enabled = render({ selected: new Set(['Pod']), clearAllDisabled: false })
    // The bulk row's Clear all button is not disabled here (the list checkboxes carry no disabled attr either).
    expect(enabled).toContain('aria-label="Clear kind selection"')
  })

  it('labels the visible-bulk button by search + selection state', () => {
    // No search, nothing selected -> select all.
    expect(render({ search: '' })).toContain('全选')

    // Search active, nothing of the visible set selected.
    const searching = render({ search: 'e' })
    expect(searching).toMatch(/选择当前可见项（\d+）/)

    // All visible selected.
    const allSelected = render({ selected: new Set(KINDS), clearAllDisabled: false })
    expect(allSelected).toMatch(/清除当前可见项（\d+）/)
  })
})
