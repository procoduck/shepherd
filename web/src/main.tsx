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

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
      <Toaster richColors position='top-right' />
    </QueryClientProvider>
  </React.StrictMode>,
);
