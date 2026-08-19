import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { AppWindow } from 'lucide-react';
import { clients } from '@/api/transport';
import { useOrgId } from '@/hooks/useOrg';

// Static gallery copy for known wizard kinds (spec §13.5: "v1 has one card").
// Keyed by the wizard `kind` returned by ListWizards so a second registered
// wizard picks up a card automatically once it's added here.
const WIZARD_CARDS: Record<string, { title: string; description: string; href: string }> = {
  'app-observability': {
    title: 'Application observability',
    description:
      "Scrape logs and metrics from selected namespaces and ship them to your org's destinations.",
    href: '/wizards/app-observability',
  },
};

export function WizardsPage() {
  const orgId = useOrgId();
  const { data, isLoading } = useQuery({
    queryKey: ['wizards', orgId],
    queryFn: () => clients.wizard.listWizards({ orgId }),
    enabled: !!orgId,
  });

  const cards = (data?.items ?? []).filter((w) => WIZARD_CARDS[w.kind]);

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
      ) : (
        <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3'>
          {cards.map((w) => {
            const card = WIZARD_CARDS[w.kind];
            return (
              <div
                key={w.kind}
                className='flex flex-col rounded-lg border border-border bg-card/40 p-5'
              >
                <AppWindow size={20} className='mb-3 text-indigo-400' />
                <h2 className='text-sm font-semibold text-zinc-100'>{card.title}</h2>
                <p className='mt-1 flex-1 text-xs text-muted'>{card.description}</p>
                <Link
                  to={card.href}
                  className='mt-4 self-start rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
                >
                  Start
                </Link>
              </div>
            );
          })}
          <div className='flex flex-col items-center justify-center rounded-lg border border-dashed border-border p-5 text-center'>
            <p className='text-xs text-muted'>More wizards coming</p>
          </div>
        </div>
      )}
    </div>
  );
}
