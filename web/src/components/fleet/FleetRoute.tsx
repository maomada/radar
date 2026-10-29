import { lazy, Suspense, type ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { useNavCustomization } from '../../context/NavCustomization'

const FleetPage = lazy(() => import('./FleetPage'))

export function FleetRoute({ children }: { children: ReactNode }) {
  const { pathname } = useLocation()
  const { embedded } = useNavCustomization()
  if (embedded || (pathname !== '/fleet' && !pathname.startsWith('/fleet/'))) return children
  return <Suspense fallback={<div className="p-8 text-theme-text-secondary" role="status">Loading Fleet Manager…</div>}><FleetPage /></Suspense>
}
