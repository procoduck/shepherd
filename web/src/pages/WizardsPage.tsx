import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { AppWindow } from 'lucide-react';
import { clients } from '@/api/transport';
import { useOrgId } from '@/hooks/useOrg';

// The catalog is whatever the backend registry serves — title and description
// come from the wizard itself.
//
// This page used to hold a hardcoded map of kinds and filter the API response
// down to the entries it recognised. The comment on that map claimed a new
// wizard "picks up a card automatically once it's added here", which is a
// contradiction, and the filter is how five registered wizards stayed
// invisible: the API returned them and the page silently dropped them. A UI
// that keeps its own copy of a backend list will eventually disagree with it,
// and the failure is quiet in exactly this direction.
export function WizardsPage() {
  const orgId = useOrgId();
  const { data, isLoading } = useQuery({
    queryKey: ['wizards', orgId],
    queryFn: () => clients.wizard.listWizards({ orgId }),
    enabled: !!orgId,
  });

  const wizards = [...(data?.items ?? [])].sort((a, b) => a.title.localeCompare(b.title));

  return (
    <div className='space-y-4'>
      <div>
        <h1 className='text-xl font-semibold'>Wizards</h1>
        <p className='text-sm text-muted'>Guided pipeline creation.</p>
      </div>

      {!orgId ? (
        <p className='text-sm text-muted'>No organisation context.</p>
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : wizards.length === 0 ? (
        // Distinct from "loading": no wizard is registered at all, which is a
        // deployment problem rather than an empty catalog.
        <p className='text-sm text-muted'>No wizards are registered on this server.</p>
      ) : (
        <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3'>
          {wizards.map((w) => (
            <div
              key={w.kind}
              className='flex flex-col rounded-lg border border-border bg-card/40 p-5'
            >
              <AppWindow size={20} className='mb-3 text-indigo-400' />
              <h2 className='text-sm font-semibold text-zinc-100'>{w.title}</h2>
              <p className='mt-1 flex-1 text-xs text-muted'>{w.description}</p>
              <Link
                to='/wizards/$kind'
                params={{ kind: w.kind }}
                className='mt-4 self-start rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
              >
                Start
              </Link>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
