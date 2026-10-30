import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Geliştirmede /api isteklerini yerel API'ye yönlendirir; üretimde nginx aynı işi yapar (frontend/nginx.conf).
export default defineConfig({
  plugins: [react()],
  server: { port: 5173, proxy: { '/api': { target: 'http://localhost:8080', rewrite: (p) => p.replace(/^\/api/, '') } } },
});
