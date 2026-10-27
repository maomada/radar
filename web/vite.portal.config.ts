import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'

export default defineConfig({
  plugins: [tailwindcss(), react()],
  build: {
    outDir: '../internal/portalui/dist',
    emptyOutDir: true,
    rolldownOptions: { input: path.resolve(import.meta.dirname, 'portal.html') },
  },
})
