import React from 'react'
import ReactDOM from 'react-dom/client'
import { Portal } from './Portal'
import { Login } from './Login'
import './portal.css'

window.addEventListener('pageshow', event => {
  if (event.persisted) window.location.reload()
})

ReactDOM.createRoot(document.getElementById('portal')!).render(
  <React.StrictMode>{window.location.pathname === '/login' ? <Login /> : <Portal />}</React.StrictMode>,
)
