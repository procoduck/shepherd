import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, Trash2, UserPlus, Users2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import type { Team } from '@/gen/shepherd/mgmt/v1/team_pb';
import { useMe } from '@/hooks/useMe';
import { useOrg } from '@/hooks/useOrg';

/**
 * Teams.
 *
 * A team owns pipelines, and owning them is what lets its members write them
 * without org-admin. Membership arrives two ways and the page's whole job is
 * to make which one visible: an IdP group (no roster exists — the group is a
 * claim inside a session, so the group's name is the only honest answer), or
 * explicit local users (a real list, editable here). A team can use both, and
 * a team with neither can still own pipelines — it just has no members yet.
 */
export function TeamsPage() {
  const { data: me } = useMe();
  const { orgId, orgs } = useOrg();
  const qc = useQueryClient();

  const role = orgs.find((o) => o.id === orgId)?.role ?? '';
  const canManage = !!me?.isAppAdmin || role === 'admin';

  const [showCreate, setShowCreate] = useState(false);
  const [createForm, setCreateForm] = useState({ name: '', idpGroupId: '' });
  const [membersOf, setMembersOf] = useState<Team | null>(null);
  const [deleteTeam, setDeleteTeam] = useState<Team | null>(null);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['teams', orgId],
    queryFn: () => clients.team.listTeams({ orgId }),
    enabled: !!orgId,
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ['teams', orgId] });
  const fail = (verb: string) => (e: unknown) =>
    toast.error(toApiError(e).message || `Failed to ${verb}`);

  const createMut = useMutation({
    mutationFn: () =>
      clients.team.createTeam({
        orgId,
        name: createForm.name.trim(),
        idpGroupId: createForm.idpGroupId.trim(),
      }),
    onSuccess: () => {
      invalidate();
      setShowCreate(false);
      setCreateForm({ name: '', idpGroupId: '' });
      toast.success('Team created');
    },
    onError: fail('create the team'),
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => clients.team.deleteTeam({ orgId, id }),
    onSuccess: () => {
      invalidate();
      setDeleteTeam(null);
      toast.success('Team deleted');
    },
    onError: fail('delete the team'),
  });

  if (!orgId) {
    return <p className='text-sm text-muted'>Select an organisation to manage its teams.</p>;
  }
  if (isLoading) return <p className='text-sm text-muted'>Loading…</p>;
  if (isError) {
    return (
      <div
        data-testid='teams-error'
        className='rounded-md border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400'
      >
        {toApiError(error).message || 'Could not load teams.'}
      </div>
    );
  }

  const teams = data?.items ?? [];

  return (
    <div className='space-y-4'>
      <div className='flex items-start justify-between gap-4'>
        <div>
          <h1 className='text-xl font-semibold'>Teams</h1>
          <p className='mt-1 text-sm text-muted'>
            A team owns pipelines; its members can edit what it owns without being an organisation
            administrator. Members come from an identity provider group, from an explicit list of
            local users, or from both.
          </p>
        </div>
        {canManage && (
          <button
            data-testid='team-new'
            onClick={() => setShowCreate(true)}
            className='flex shrink-0 items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
          >
            <Plus size={14} /> New team
          </button>
        )}
      </div>

      {teams.length === 0 ? (
        <div
          data-testid='teams-empty'
          className='rounded-lg border border-border p-8 text-center text-sm text-muted'
        >
          <Users2 size={24} className='mx-auto mb-2 text-muted-3' />
          <p className='font-medium'>No teams</p>
          <p className='mt-1 text-muted-2'>
            Pipelines with no owning team can be edited only by organisation administrators.
          </p>
        </div>
      ) : (
        <div className='rounded-lg border border-border overflow-hidden'>
          <table className='w-full text-sm'>
            <thead className='bg-card text-muted'>
              <tr>
                <th className='px-4 py-3 text-left font-medium'>Team</th>
                <th className='px-4 py-3 text-left font-medium'>Membership</th>
                <th className='px-4 py-3' />
              </tr>
            </thead>
            <tbody>
              {teams.map((t) => (
                <tr
                  key={t.id}
                  data-testid={`team-row-${t.name}`}
                  className='border-t border-border'
                >
                  <td className='px-4 py-3 font-medium'>{t.name}</td>
                  <td className='px-4 py-3'>
                    <div className='flex flex-wrap items-center gap-1.5'>
                      {t.idpGroupId && (
                        <span
                          data-testid={`team-source-group-${t.name}`}
                          className='inline-flex items-center gap-1 rounded bg-sky-500/15 px-1.5 py-0.5 text-xs text-sky-300'
                          title='Anyone whose identity provider token carries this group is a member'
                        >
                          group <span className='font-mono'>{t.idpGroupId}</span>
                        </span>
                      )}
                      {t.memberCount > 0 && (
                        <span
                          data-testid={`team-source-members-${t.name}`}
                          className='inline-flex items-center gap-1 rounded bg-emerald-500/15 px-1.5 py-0.5 text-xs text-emerald-300'
                        >
                          {t.memberCount} {t.memberCount === 1 ? 'member' : 'members'}
                        </span>
                      )}
                      {!t.idpGroupId && t.memberCount === 0 && (
                        <span className='text-xs text-muted-2'>no members yet</span>
                      )}
                    </div>
                  </td>
                  <td className='px-4 py-3 text-right whitespace-nowrap'>
                    {canManage && (
                      <>
                        <button
                          data-testid={`team-members-${t.name}`}
                          onClick={() => setMembersOf(t)}
                          title='Manage members'
                          className='mr-2 text-muted-3 hover:text-zinc-200'
                        >
                          <UserPlus size={15} />
                        </button>
                        <button
                          data-testid={`team-delete-${t.name}`}
                          onClick={() => setDeleteTeam(t)}
                          title='Delete'
                          className='text-muted-3 hover:text-red-400'
                        >
                          <Trash2 size={15} />
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {showCreate && (
        <AdminModal title='New team' onClose={() => setShowCreate(false)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createMut.mutate();
            }}
            className='space-y-3'
          >
            <label className='block text-xs font-medium text-muted'>
              Name
              <input
                data-testid='team-name'
                value={createForm.name}
                onChange={(e) => setCreateForm((f) => ({ ...f, name: e.target.value }))}
                required
                className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm'
                placeholder='platform'
              />
            </label>
            <label className='block text-xs font-medium text-muted'>
              Identity provider group <span className='text-muted-3'>(optional)</span>
              <input
                data-testid='team-group'
                value={createForm.idpGroupId}
                onChange={(e) => setCreateForm((f) => ({ ...f, idpGroupId: e.target.value }))}
                className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm font-mono'
                placeholder='platform-engineers'
              />
              <span className='mt-1 block text-2xs font-normal text-muted-3'>
                Whatever your provider emits in the groups claim. Leave it empty to build the team
                from local users instead — you can add them once it exists.
              </span>
            </label>
            <AdminModalActions
              onCancel={() => setShowCreate(false)}
              submitLabel='Create'
              pendingLabel='Creating…'
              pending={createMut.isPending}
            />
          </form>
        </AdminModal>
      )}

      {membersOf && (
        <TeamMembersModal orgId={orgId} team={membersOf} onClose={() => setMembersOf(null)} />
      )}

      {deleteTeam && (
        <AdminConfirmDialog
          title='Delete team'
          body={`Delete "${deleteTeam.name}"? Pipelines it owns are not deleted — they become unowned, editable only by organisation administrators.`}
          confirmLabel='Delete'
          pendingLabel='Deleting…'
          pending={deleteMut.isPending}
          onCancel={() => setDeleteTeam(null)}
          onConfirm={() => deleteMut.mutate(deleteTeam.id)}
        />
      )}
    </div>
  );
}

/**
 * The explicit half of a team's membership. Group-derived members are
 * deliberately absent: there is no roster to show for them, and rendering an
 * empty list beside a configured group would read as "this group has nobody
 * in it" rather than "membership lives in your identity provider".
 */
function TeamMembersModal({
  orgId,
  team,
  onClose,
}: {
  orgId: string;
  team: Team;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [addUserId, setAddUserId] = useState('');

  const { data, isLoading } = useQuery({
    queryKey: ['team-members', team.id],
    queryFn: () => clients.team.listTeamMembers({ orgId, teamId: team.id }),
  });

  // Every local account, to offer as candidates. App-admin only, so a
  // non-app-admin org admin sees the roster but gets a free-text id field
  // rather than a picker they are not allowed to populate.
  const { data: users } = useQuery({
    queryKey: ['users'],
    queryFn: () => clients.user.listUsers({}),
    retry: false,
  });

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['team-members', team.id] });
    qc.invalidateQueries({ queryKey: ['teams', orgId] });
  };
  const fail = (verb: string) => (e: unknown) =>
    toast.error(toApiError(e).message || `Failed to ${verb}`);

  const addMut = useMutation({
    mutationFn: (userId: string) => clients.team.addTeamMember({ orgId, teamId: team.id, userId }),
    onSuccess: () => {
      invalidate();
      setAddUserId('');
      toast.success('Member added');
    },
    onError: fail('add the member'),
  });

  const removeMut = useMutation({
    mutationFn: (userId: string) =>
      clients.team.removeTeamMember({ orgId, teamId: team.id, userId }),
    onSuccess: () => {
      invalidate();
      toast.success('Member removed');
    },
    onError: fail('remove the member'),
  });

  const members = data?.items ?? [];
  const memberIds = new Set(members.map((m) => m.userId));
  const candidates = (users?.items ?? []).filter((u) => !memberIds.has(u.id));

  return (
    <AdminModal title={`Members of ${team.name}`} onClose={onClose}>
      <div className='space-y-4'>
        {team.idpGroupId && (
          <p
            data-testid='team-members-group-note'
            className='rounded-md border border-sky-500/30 bg-sky-500/10 p-2.5 text-xs text-sky-200'
          >
            Anyone in the group <span className='font-mono'>{team.idpGroupId}</span> is already a
            member. Those people are not listed here — membership lives in your identity provider,
            not in Shepherd. Anyone added below is a member in addition to them.
          </p>
        )}

        {isLoading ? (
          <p className='text-sm text-muted'>Loading…</p>
        ) : members.length === 0 ? (
          <p data-testid='team-members-empty' className='text-sm text-muted-2'>
            No individually added members.
          </p>
        ) : (
          <ul className='divide-y divide-border rounded-md border border-border'>
            {members.map((m) => (
              <li
                key={m.userId}
                data-testid={`team-member-${m.login}`}
                className='flex items-center justify-between px-3 py-2'
              >
                <span className='text-sm'>
                  <span className='font-medium'>{m.login}</span>
                  {m.displayName && (
                    <span className='ml-2 text-xs text-muted-2'>{m.displayName}</span>
                  )}
                  {m.disabled && (
                    <span className='ml-2 rounded bg-red-500/15 px-1.5 py-0.5 text-2xs text-red-400'>
                      disabled
                    </span>
                  )}
                </span>
                <button
                  data-testid={`team-member-remove-${m.login}`}
                  onClick={() => removeMut.mutate(m.userId)}
                  disabled={removeMut.isPending}
                  title='Remove from team'
                  className='text-muted-3 hover:text-red-400 disabled:opacity-50'
                >
                  <Trash2 size={15} />
                </button>
              </li>
            ))}
          </ul>
        )}

        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (addUserId) addMut.mutate(addUserId);
          }}
          className='flex items-end gap-2'
        >
          <label className='block flex-1 text-xs font-medium text-muted'>
            Add a local user
            {users ? (
              <select
                data-testid='team-member-add-select'
                value={addUserId}
                onChange={(e) => setAddUserId(e.target.value)}
                className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm'
              >
                <option value=''>Select a user…</option>
                {candidates.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.login}
                    {u.displayName ? ` — ${u.displayName}` : ''}
                  </option>
                ))}
              </select>
            ) : (
              <input
                data-testid='team-member-add-input'
                value={addUserId}
                onChange={(e) => setAddUserId(e.target.value)}
                placeholder='user id'
                className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm font-mono'
              />
            )}
          </label>
          <button
            data-testid='team-member-add'
            type='submit'
            disabled={!addUserId || addMut.isPending}
            className='rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
          >
            {addMut.isPending ? 'Adding…' : 'Add'}
          </button>
        </form>
      </div>
    </AdminModal>
  );
}
