import { useQueryClient } from '@tanstack/react-query';
import { Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router';
import {
  Building2,
  ChevronLeft,
  ChevronRight,
  ClipboardList,
  GitBranch,
  Key,
  KeyRound,
  LayoutDashboard,
  Moon,
  Radio,
  Route,
  Send,
  Server,
  Shield,
  Sun,
  Users,
  Users2,
  Wand2,
  Workflow,
} from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { buildCrumbs } from '@/components/breadcrumb';
import { useCrumbNames } from '@/components/useCrumbNames';
import { useMe } from '@/hooks/useMe';
import { useOrg } from '@/hooks/useOrg';
import { currentReturnPath, loginHref } from '@/lib/returnPath';
import { cn } from '@/lib/utils';
import { requiredRoleFor, roleSatisfied, routeManifest } from '@/routes/routeManifest';
import { applyTheme, resolveTheme, setStoredTheme, type Theme } from '@/theme';

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
      { label: 'Git sync', href: '/git', icon: <GitBranch size={16} /> },
      { label: 'Tenant routes', href: '/tenant-routes', icon: <Route size={16} /> },
    ],
  },
  {
    label: 'Access',
    items: [
      { label: 'Teams', href: '/teams', icon: <Users2 size={16} /> },
      { label: 'Service accounts', href: '/service-accounts', icon: <KeyRound size={16} /> },
    ],
  },
  {
    label: 'Admin',
    items: [
      {
        label: 'Organisations',
        href: '/admin/orgs',
        icon: <Building2 size={16} />,
        adminOnly: true,
      },
      { label: 'Clusters', href: '/admin/clusters', icon: <Radio size={16} />, adminOnly: true },
      { label: 'Agent Tokens', href: '/admin/tokens', icon: <Key size={16} />, adminOnly: true },
      { label: 'Users', href: '/admin/users', icon: <Users size={16} />, adminOnly: true },
      {
        label: 'Single sign-on',
        href: '/admin/auth',
        icon: <KeyRound size={16} />,
        adminOnly: true,
      },
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
  // The user's own preference, persisted. The builder's override below is layered
  // on top of it and deliberately never written here.
  const [userCollapsed, setUserCollapsed] = useState(
    () => localStorage.getItem('sidebar-collapsed') === '1',
  );
  const [theme, setTheme] = useState<Theme>(() => resolveTheme());
  // D8: only an explicit toggle click persists a choice — reflecting the
  // resolved (possibly system-derived) theme on mount must not silently
  // turn "no preference set" into a stored one. See src/theme.ts.
  const isFirstThemeEffect = useRef(true);

  // All effects must be before any conditional return (Rules of Hooks)
  useEffect(() => {
    if (!isLoading && me === null) {
      // #250: remember where the user was going, so signing in returns there.
      window.location.href = loginHref(currentReturnPath());
    }
  }, [me, isLoading]);

  useEffect(() => {
    applyTheme(theme);
    if (isFirstThemeEffect.current) {
      isFirstThemeEffect.current = false;
      return;
    }
    setStoredTheme(theme);
  }, [theme]);

  useEffect(() => {
    localStorage.setItem('sidebar-collapsed', userCollapsed ? '1' : '0');
  }, [userCollapsed]);

  // The visual builder is a working surface, so the app chrome gets out of its way —
  // draw.io gives the canvas the whole window. On a builder route the nav renders as
  // its rail, recovering 184px of canvas on top of what the builder's own collapsible
  // panels free.
  //
  // This is an OVERRIDE, not a preference change: it is never written to
  // localStorage, so a user who likes an expanded nav still gets one everywhere else,
  // even if they close the tab while inside the builder. Expanding it inside the
  // builder is respected for as long as they stay there.
  const isBuilderRoute = /^\/pipelines\/(visual\/new|[^/]+\/(visual|graph))$/.test(
    location.pathname,
  );
  const [builderOverride, setBuilderOverride] = useState<boolean | null>(null);
  useEffect(() => {
    if (!isBuilderRoute) setBuilderOverride(null);
  }, [isBuilderRoute]);

  const collapsed = isBuilderRoute ? (builderOverride ?? true) : userCollapsed;
  const setCollapsed = (next: boolean) => {
    if (isBuilderRoute) setBuilderOverride(next);
    else setUserCollapsed(next);
  };

  async function handleLogout() {
    await fetch('/auth/logout', { headers: { 'X-Requested-With': 'XMLHttpRequest' } });
    queryClient.clear();
    // Clear the test-injected initialData so LoginPage doesn't auto-redirect after logout.
    if (typeof window !== 'undefined') {
      delete (window as unknown as Record<string, unknown>).__initialMe;
    }
    navigate({ to: '/login' });
  }

  // #250: detail routes name their item (the pipeline, collector or wizard)
  // instead of the generic "Pipeline" / "Collector" / "Wizard" label.
  const resolveCrumbName = useCrumbNames(location.pathname, orgId);
  const crumbs = buildCrumbs(location.pathname, routeManifest, resolveCrumbName);

  if (isLoading) return null;
  if (!me) return null;
  return (
    <div className='flex h-screen overflow-hidden bg-background text-zinc-100'>
      {/* Sidebar */}
      <aside
        data-testid='app-sidebar'
        data-collapsed={collapsed ? 'true' : 'false'}
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
            // A link is offered only when its route's requiredRole (routeManifest,
            // the same table RequireRole enforces) is met in the selected org —
            // otherwise it led to a page that refused (#206).
            const visibleItems = group.items.filter((item) => {
              if (item.adminOnly && !me.isAppAdmin) return false;
              const required = requiredRoleFor(item.href);
              return !required || roleSatisfied(me, orgId, required);
            });
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
          onClick={() => setCollapsed(!collapsed)}
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
              {crumbs.map((crumb, i) => (
                // Crumbs have no stable id of their own (a label can repeat,
                // e.g. two "Pipeline" crumbs are impossible today but not
                // structurally ruled out) — position is the only key that
                // survives a route change without churn.
                <span key={i}>
                  {i > 0 && ' / '}
                  {crumb.label}
                </span>
              ))}
            </span>
          </nav>
          <div className='flex items-center gap-2'>
            {/* #250: a user in one org has nothing to switch, but should
                still see which org they are working in. */}
            {orgs.length === 1 && (
              <span
                data-testid='org-name'
                title='Organization'
                className='flex items-center gap-1.5 rounded-md border border-border px-2 py-1 text-xs text-muted'
              >
                <Building2 size={12} className='shrink-0' aria-hidden='true' />
                {orgs[0].displayName || orgs[0].name}
              </span>
            )}
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
              onClick={() => setTheme((t) => (t === 'dark' ? 'light' : 'dark'))}
              className='p-1.5 rounded-md text-muted hover:text-zinc-100 hover:bg-border/40'
              aria-label='Toggle theme'
            >
              {theme === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
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
          <div className='flex-1 min-h-0'>
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  );
}
