import { Link } from '@tanstack/react-router';

/**
 * The 404 for a URL no route matches (B7, 2026-10-09 walkthrough). Wired as
 * the content layout's `notFoundComponent` (routes/router.tsx), so it renders
 * inside the shell — header, sidebar and all — instead of TanStack Router's
 * bare "Not Found", which left the address bar as the only way back.
 */
export function NotFoundPage() {
  return (
    <div data-testid='not-found' className='space-y-2'>
      <h1 className='text-xl font-semibold'>Page not found</h1>
      <p className='text-sm text-muted'>
        Nothing lives at this address. It may have been mistyped, or the page it pointed to no
        longer exists.
      </p>
      <Link to='/' className='text-sm text-indigo-400 hover:text-indigo-300'>
        Go to the overview
      </Link>
    </div>
  );
}
