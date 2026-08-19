import { useQueryClient } from '@tanstack/react-query';
import { Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router';
import {
  Building2,
  ChevronLeft,
  ChevronRight,
  ClipboardList,
  GitBranch,
  Key,
  LayoutDashboard,
  Moon,
  Radio,
  Send,
  Server,
  Shield,
  Sun,
  TriangleAlert,
  Wand2,
  Workflow,
} from 'lucide-react';
import { useEffect, useState } from 'react';
import { useMe } from '@/hooks/useMe';
import { useOrg } from '@/hooks/useOrg';
import { cn } from '@/lib/utils';

interface NavItem {
  label: string;
  href: string;
  icon: React.ReactNode;
  adminOnly?: boolean;
}

const navGroups: Array<{ label?: string; items: NavItem[] }> = [
  {
    items: [{ label: 'Overview', href: '/', icon: <LayoutDashboard size={16} /> }],
  },
  {
    label: 'Fleet',
    items: [
      { label: 'Collectors', href: '/collectors', icon: <Server size={16} /> },
      { label: 'Pipelines', href: '/pipelines', icon: <Workflow size={16} /> },
      { label: 'Wizards', href: '/wizards', icon: <Wand2 size={16} /> },
    ],
  },
  {
    label: 'Delivery',
    items: [
      { label: 'Destinations', href: '/destinations', icon: <Send size={16} /> },
      { label: 'Git', href: '/git', icon: <GitBranch size={16} /> },
    ],
  },
  {
    label: 'Admin',
    items: [
      { label: 'Orgs', href: '/admin/orgs', icon: <Building2 size={16} />, adminOnly: true },
      { label: 'Clusters', href: '/admin/clusters', icon: <Radio size={16} />, adminOnly: true },
      { label: 'Agent Tokens', href: '/admin/tokens', icon: <Key size={16} />, adminOnly: true },
    ],
  },
  {
    items: [{ label: 'Audit', href: '/audit', icon: <ClipboardList size={16} /> }],
  },
];

export function Shell() {
  const location = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: me, isLoading } = useMe();
  const { orgId, orgs, setOrgId } = useOrg();
  const [collapsed, setCollapsed] = useState(
    () => localStorage.getItem('sidebar-collapsed') === '1',
  );
  const [dark, setDark] = useState(() => localStorage.getItem('theme') !== 'light');

  // All effects must be before any conditional return (Rules of Hooks)
  useEffect(() => {
    if (!isLoading && me === null) {
      window.location.href = '/login';
    }
  }, [me, isLoading]);

  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark);
    localStorage.setItem('theme', dark ? 'dark' : 'light');
  }, [dark]);

  useEffect(() => {
    localStorage.setItem('sidebar-collapsed', collapsed ? '1' : '0');
  }, [collapsed]);

  async function handleLogout() {
    await fetch('/auth/logout', { headers: { 'X-Requested-With': 'XMLHttpRequest' } });
    queryClient.clear();
    // Clear the test-injected initialData so LoginPage doesn't auto-redirect after logout.
    if (typeof window !== 'undefined') {
      delete (window as unknown as Record<string, unknown>).__initialMe;
    }
    navigate({ to: '/login' });
  }

  if (isLoading) return null;
  if (!me) return null;
  return (
    <div className='flex h-screen overflow-hidden bg-background text-zinc-100'>
      {/* Sidebar */}
      <aside
        className={cn(
          'flex flex-col border-r border-border transition-all duration-200',
          collapsed ? 'w-14' : 'w-60',
        )}
      >
        {/* Logo */}
        <div className='flex h-14 items-center gap-2 border-b border-border px-3'>
          <Shield size={18} className='shrink-0 text-indigo-500' />
          {!collapsed && <span className='text-sm font-semibold tracking-tight'>Shepherd</span>}
        </div>

        {/* Nav */}
        <nav className='flex-1 overflow-y-auto py-3 px-2 space-y-4'>
          {navGroups.map((group, gi) => {
            const visibleItems = group.items.filter((item) => !item.adminOnly || me.isAppAdmin);
            if (visibleItems.length === 0) return null;
            return (
              <div key={gi}>
                {group.label && !collapsed && (
                  <p className='mb-1 px-2 text-xs uppercase text-muted-2 font-medium'>
                    {group.label}
                  </p>
                )}
                {visibleItems.map((item) => {
                  const active =
                    location.pathname === item.href ||
                    (item.href !== '/' && location.pathname.startsWith(item.href));
                  return (
                    <Link
                      key={item.href}
                      to={item.href}
                      className={cn(
                        'flex h-9 items-center gap-2 rounded-md px-2 text-sm transition-colors relative',
                        active
                          ? 'bg-border/60 text-zinc-100'
                          : 'text-muted hover:text-zinc-100 hover:bg-border/40',
                      )}
                      title={collapsed ? item.label : undefined}
                    >
                      {active && (
                        <span className='absolute left-0 top-1.5 bottom-1.5 w-0.5 bg-indigo-500 rounded-full' />
                      )}
                      <span className='shrink-0'>{item.icon}</span>
                      {!collapsed && <span>{item.label}</span>}
                    </Link>
                  );
                })}
              </div>
            );
          })}
        </nav>

        {/* Collapse toggle */}
        <button
          onClick={() => setCollapsed((c) => !c)}
          className='flex h-10 items-center justify-center border-t border-border text-muted-2 hover:text-zinc-100'
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
        >
          {collapsed ? <ChevronRight size={16} /> : <ChevronLeft size={16} />}
        </button>
      </aside>

      {/* Main */}
      <div className='flex flex-1 flex-col overflow-hidden'>
        {/* Topbar */}
        <header className='flex h-14 shrink-0 items-center justify-between border-b border-border px-6'>
          <nav aria-label='breadcrumb'>
            <span className='text-sm text-muted'>
              {location.pathname === '/'
                ? 'Overview'
                : location.pathname.replace(/^\//, '').replace(/\//g, ' / ')}
            </span>
          </nav>
          <div className='flex items-center gap-2'>
            {orgs.length > 1 && (
              <select
                data-testid='org-switcher'
                aria-label='Switch organization'
                value={orgId}
                onChange={(e) => setOrgId(e.target.value)}
                className='rounded-md border border-border-strong bg-card px-2 py-1 text-xs text-zinc-100 focus:outline-none focus:ring-1 focus:ring-indigo-500'
              >
                {orgs.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.displayName || o.name}
                  </option>
                ))}
              </select>
            )}
            <button
              onClick={() => setDark((d) => !d)}
              className='p-1.5 rounded-md text-muted hover:text-zinc-100 hover:bg-border/40'
              aria-label='Toggle theme'
            >
              {dark ? <Sun size={16} /> : <Moon size={16} />}
            </button>
            <button
              onClick={handleLogout}
              className='text-xs text-muted hover:text-zinc-100 px-2 py-1 rounded hover:bg-border/40'
            >
              Sign out
            </button>
          </div>
        </header>

        {/* Content */}
        <main className='flex flex-1 flex-col min-h-0'>
          {me.authMethod === 'local' && (
            <div
              data-testid='local-admin-banner'
              role='alert'
              className='flex shrink-0 items-center gap-2 border-b border-amber-500/30 bg-amber-500/10 px-6 py-3 text-sm text-amber-300'
            >
              <TriangleAlert size={16} />
              <span>
                Signed in with the local break-glass account — restore your identity provider access
                and sign out.
              </span>
            </div>
          )}
          <div className='flex-1 min-h-0'>
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  );
}
