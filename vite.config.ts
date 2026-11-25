import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'path';

const proxy = {
  '/api': {
    target: 'http://127.0.0.1:8787',
    // Keep the original Host header so the backend's origin guard treats this as same-origin
    // when developing via Vite (avoids requiring the backend to run with --dev).
    changeOrigin: false,
  },
};

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: [
      { find: '@', replacement: path.resolve(__dirname, '.') },
      { find: /^highcharts-react-official$/, replacement: path.resolve(__dirname, 'stubs/highcharts-react-official/index.tsx') },
      { find: /^highcharts$/, replacement: path.resolve(__dirname, 'stubs/highcharts/index.ts') },
      { find: /^highcharts\/.+$/, replacement: path.resolve(__dirname, 'stubs/highcharts/module-stub.ts') },
    ],
  },
  build: {
    // The Go backend embeds this directory when built with `-tags=embedui`.
    outDir: 'server/internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    proxy,
  },
  preview: {
    proxy,
  },
});
