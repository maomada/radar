import type { SearchMatchedField } from '../../api/client'

// contentMatchLabel names where a hit matched when every token landed in the
// object's content rather than its name, kind or namespace. Trivy Operator
// reports keep their findings under `report`, not a spec.
export function contentMatchLabel(matched: SearchMatchedField[] | undefined): string {
  if (!matched?.length || !matched.every((m) => m.site.startsWith('content:'))) return ''
  return matched.every((m) => m.site.startsWith('content:report.')) ? 'in findings' : 'in spec'
}
