import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { execSync } from 'child_process';
import { writeFileSync } from 'fs';
import path from 'path';
import { defineConfig } from 'vite';

const buildInfoPlugin = {
  name: 'build-info',
  closeBundle() {
    const sha = (() => {
      try {
        return execSync('git rev-parse --short HEAD').toString().trim();
      } catch {
        return 'dev';
      }
    })();
    const dirty = (() => {
      try {
        return execSync('git status --porcelain').toString().trim() !== '';
      } catch {
        return true;
      }
    })();
    writeFileSync(
      '../internal/spa/dist/BUILD_INFO.json',
      JSON.stringify({
        git_sha: sha,
        built_at: new Date().toISOString(),
        dirty,
      }),
    );
  },
};

export default defineConfig({
  plugins: [react(), tailwindcss(), buildInfoPlugin],
  resolve: {
    // `import.meta.dirname`, not `__dirname`: Vite 8 warns that this config
    // uses features its upcoming default `configLoader: 'native'` cannot
    // provide — under that loader Node imports this file as plain ESM, where
    // the CommonJS `__dirname` shim does not exist. `import.meta.dirname` is
    // the ESM equivalent (Node 20.11+; this repo runs Node 24) and works under
    // the bundled loader too, so there is nothing to suppress.
    alias: { '@': path.resolve(import.meta.dirname, './src') },
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/auth': 'http://localhost:8080',
    },
  },
  build: {
    outDir: '../internal/spa/dist',
    emptyOutDir: true,
    // Measured 2026-09-11 on vite 8.3.0 / rolldown 1.2.8 (minified, kB = 1000 B):
    // the entry chunk index-*.js is 894 kB (gzip 274 kB); the lazy visual-builder
    // route chunks are PipelineNode 206 kB, VisualBuilderPage 72 kB and
    // GraphViewPage 4 kB, plus a 177 kB shared chunk (@bufbuild/protobuf + the
    // Connect codegen) that rolldown's default splitting carves out on its own.
    // Under vite 6 / rollup the same entry was 1,020 kB, so the migration already
    // shrank it; the entry is the only chunk over the 500 kB default.
    //
    // The entry is what every page needs on first paint — react-dom (~250 kB),
    // the pipeline editor's CodeMirror (~250 kB), TanStack router + query and
    // the statically imported pages. Regrouping it into vendor chunks through
    // build.rolldownOptions.output.codeSplitting would not reduce first-load
    // bytes, and the SPA ships content-hashed inside the Go binary per release
    // (deps bump alongside app code), so cross-release cache reuse of a vendor
    // chunk is worth little. The limit therefore sits just above the measured
    // entry: the one regression this warning can still catch here — a new
    // dependency landing statically in the shell — trips it. Moving CodeMirror
    // behind a lazy route would cut the entry by about a quarter; that is a
    // source-level change, not a bundler setting.
    chunkSizeWarningLimit: 1000,
  },
  test: {
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
    exclude: ['tests/**', 'node_modules/**', 'dist/**'],
    // Environment stays 'node' by default (the 16 pure-TS tests keep their
    // speed); component tests opt into jsdom with a per-file
    // `// @vitest-environment jsdom` docblock instead.
    //
    // `globals: true` is needed for exactly one thing: @testing-library/react
    // registers its automatic cleanup() via `afterEach` only when `afterEach`
    // already exists on globalThis at import time (see its index.js). Without
    // this, unmounted components from one test's render() pile up in the
    // jsdom document and leak into the next test in the same file.
    globals: true,
  },
});
