import React from 'react'
import ReactDOM from 'react-dom/client'
import { Portal } from './Portal'
import './portal.css'

ReactDOM.createRoot(document.getElementById('portal')!).render(
  <React.StrictMode><Portal /></React.StrictMode>,
)
