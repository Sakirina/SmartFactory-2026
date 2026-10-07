import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig(({ mode }) => {
  const backend = loadEnv(mode, '.', 'SF_').SF_API_URL || 'http://127.0.0.1:8090';
  const proxy = { '/api': { target: backend, changeOrigin: false }, '/mcp': { target: backend, changeOrigin: false } };
  return {
    plugins: [react()],
    server: { proxy },
    preview: { proxy },
    build: { outDir: loadEnv(mode, '.', 'SF_').SF_BUILD_DIR || 'dist', emptyOutDir: true, rolldownOptions: { input: ['index.html', 'edge.html', 'screen.html'] } },
  };
});
