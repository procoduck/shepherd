import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { execSync } from 'child_process';
import { writeFileSync } from 'fs';
import path from 'path';
import { defineConfig } from 'vite';

const buildInfoPlugin = {
  name: 'build-info',
  // Build only: under Vite 8 closeBundle also fires when the dev server shuts
  // down, which rewrote the tracked internal/spa/dist/BUILD_INFO.json at the
  // end of every `make dev-frontend` session. src/api/devServer.test.ts pins it.
  apply: 'build' as const,
  closeBundle() {
    // The Docker web stage (deploy/Dockerfile.local) builds the SPA with no
    // git and no .git in the context, so these commands fail there — and
    // execSync inherits the child's stderr, which printed "/bin/sh: 1: git:
    // not found" into every image build. `stdio: ['ignore','pipe','ignore']`
    // silences that; the catch still yields the fallback. A caller that has
    // the sha (a build with git, or a --build-arg) can inject it through
    // SHEPHERD_BUILD_SHA / SHEPHERD_BUILD_DIRTY instead, so the embedded
    // BUILD_INFO carries a real sha for the runtime staleness check
    // (internal/server/server.go) without shelling out at all.
    const gitQuiet = (args: string) =>
      execSync(args, { stdio: ['ignore', 'pipe', 'ignore'] })
        .toString()
        .trim();
    const envSha = process.env.SHEPHERD_BUILD_SHA?.trim();
    const envDirty = process.env.SHEPHERD_BUILD_DIRTY?.trim();
    const sha = (() => {
      if (envSha) return envSha;
      try {
        return gitQuiet('git rev-parse --short HEAD');
      } catch {
        return 'dev';
      }
    })();
    const dirty = (() => {
      if (envDirty) return envDirty === '1' || envDirty.toLowerCase() === 'true';
      try {
        return gitQuiet('git status --porcelain') !== '';
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
    // `make dev-frontend`: everything the SPA sends to its own origin that the
    // backend must answer. The Connect API is the one people forget — the
    // transport posts to /shepherd.mgmt.v1.<Service>/<Method> with baseUrl
    // '/', so without the third entry login works and every screen after it
    // fails. src/api/devServer.test.ts pins all three.
    proxy: {
      '/api': 'http://localhost:8080',
      '/auth': 'http://localhost:8080',
      '^/shepherd\\.mgmt\\.v1\\.': 'http://localhost:8080',
    },
  },
  build: {
    outDir: '../internal/spa/dist',
    emptyOutDir: true,
    // Measured 2026-09-11 on vite 8.3.0 / rolldown 1.2.8 (minified, kB = 1000 B):
    // the entry chunk index-*.js is 688 kB (gzip 200 kB). The lazy chunks are
    // AlloyEditor 383 kB (CodeMirror + the Lezer grammar + the Alloy language
    // and completion sources, behind src/editor/LazyAlloyEditor — fetched when
    // an editor first mounts), PipelineNode 206 kB, VisualBuilderPage 72 kB and
    // GraphViewPage 4 kB. Before the editor was split out the entry was 894 kB
    // (gzip 274 kB); under vite 6 / rollup, 1,020 kB.
    //
    // The entry is what every page needs on first paint — react-dom (~250 kB),
    // TanStack router + query, the Connect/protobuf codegen and the statically
    // imported pages. Regrouping it into vendor chunks through
    // build.rolldownOptions.output.codeSplitting would not reduce first-load
    // bytes, and the SPA ships content-hashed inside the Go binary per release
    // (deps bump alongside app code), so cross-release cache reuse of a vendor
    // chunk is worth little. The limit therefore sits just above the measured
    // entry: the one regression this warning can still catch here — a new
    // dependency landing statically in the shell, or CodeMirror finding its
    // way back in through a static import — trips it.
    chunkSizeWarningLimit: 750,
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
