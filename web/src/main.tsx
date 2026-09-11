import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import React from 'react';
import ReactDOM from 'react-dom/client';
import { Toaster } from 'sonner';
import { router } from './routes/router';
import './index.css';

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, staleTime: 30_000 } },
});

// Pre-populate /api/me data from test injection (set via addInitScript in mocked tests).
// In production __initialMe is never set. This eliminates the async round-trip on first
// render so org-scoped queries can fire immediately.
if (typeof window !== 'undefined') {
  const initialMe = (window as unknown as Record<string, unknown>).__initialMe;
  if (initialMe) {
    queryClient.setQueryData(['me'], initialMe);
  }
}

ReactDOM.createRoot(document.getElementById('root')!, {
  // React 19 reports every error an error boundary catches through this hook,
  // and its default is console.error — the same channel as an unhandled
  // error (React 18 logged nothing on this path). A boundary-caught error is a
  // handled one here: the router's RouteErrorFallback (routes/router.tsx) has
  // already rendered it for the user, so it is logged as a warning for
  // diagnostics rather than as a second, unhandled-looking error. The mocked
  // Playwright suite's console-error guard (tests/fixtures/test.ts) enforces
  // exactly that split for the route-chunk-failure case in states.spec.ts.
  onCaughtError: (error, errorInfo) => {
    console.warn('Error handled by an error boundary:', error, errorInfo.componentStack);
  },
}).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
      <Toaster richColors position='top-right' />
    </QueryClientProvider>
  </React.StrictMode>,
);
