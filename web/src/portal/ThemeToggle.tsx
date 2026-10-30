import { useEffect, useState } from 'react'
import { Moon, Sun } from 'lucide-react'

export function ThemeToggle() {
  const [dark, setDark] = useState(() => {
    try { return localStorage.getItem('radar-portal-theme') === 'dark' }
    catch { return false }
  })
  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
    try { localStorage.setItem('radar-portal-theme', dark ? 'dark' : 'light') } catch { /* Storage may be disabled. */ }
  }, [dark])
  return <button type="button" className="portal-control" onClick={() => setDark(value => !value)} aria-label={dark ? '切换浅色主题' : '切换深色主题'}>{dark ? <Sun size={17} /> : <Moon size={17} />}</button>
}
