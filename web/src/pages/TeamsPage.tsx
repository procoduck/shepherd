import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, Trash2, UserPlus, Users2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import { QueryError } from '@/components/QueryError';
import { Banner } from '@/components/ui/Banner';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import { Field, Input, Select } from '@/components/ui/Field';
import { FormError } from '@/components/ui/FormError';
import { toneClass } from '@/components/ui/statusTone';
import type { Team } from '@/gen/shepherd/mgmt/v1/team_pb';
import { useMe } from '@/hooks/useMe';
import { useOrg } from '@/hooks/useOrg';
import { formError } from '@/lib/formError';

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
function teamColumns(
  canManage: boolean,
  setMembersOf: (t: Team) => void,
  setDeleteTeam: (t: Team) => void,
): DataTableColumn<Team>[] {
  return [
    {
      key: 'name',
      header: 'Team',
      headerClassName: 'px-4 py-3 text-left font-medium',
      cellClassName: 'px-4 py-3 font-medium',
      render: (t) => t.name,
    },
    {
      key: 'membership',
      header: 'Membership',
      headerClassName: 'px-4 py-3 text-left font-medium',
      cellClassName: 'px-4 py-3',
      render: (t) => (
        <div className='flex flex-wrap items-center gap-1.5'>
          {t.idpGroupId && (
            <span
              data-testid={`team-source-group-${t.name}`}
              className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs ${toneClass('info')}`}
              title='Anyone whose identity provider token carries this group is a member'
            >
              group <span className='font-mono'>{t.idpGroupId}</span>
            </span>
          )}
          {t.memberCount > 0 && (
            <span
              data-testid={`team-source-members-${t.name}`}
              className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs ${toneClass('ok')}`}
            >
              {t.memberCount} {t.memberCount === 1 ? 'member' : 'members'}
            </span>
          )}
          {!t.idpGroupId && t.memberCount === 0 && (
            <span className='text-xs text-muted-2'>no members yet</span>
          )}
        </div>
      ),
    },
    {
      key: 'actions',
      header: '',
      headerClassName: 'px-4 py-3',
      cellClassName: 'px-4 py-3 text-right whitespace-nowrap',
      render: (t) =>
        canManage && (
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
        ),
    },
  ];
}

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
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => clients.team.deleteTeam({ orgId, id }),
    onSuccess: () => {
      invalidate();
      setDeleteTeam(null);
      toast.success('Team deleted');
    },
  });
  const closeCreate = () => {
    setShowCreate(false);
    createMut.reset();
  };

  if (!orgId) {
    return <p className='text-sm text-muted'>Select an organisation to manage its teams.</p>;
  }
  if (isLoading) return <p className='text-sm text-muted'>Loading…</p>;
  if (isError) {
    return <QueryError error={error} noun='teams' testId='teams-error' />;
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
            Pipelines with no owning team can be edited only by organisation admins and editors.
          </p>
        </div>
      ) : (
        <DataTable
          columns={teamColumns(canManage, setMembersOf, setDeleteTeam)}
          rows={teams}
          rowKey={(t) => t.id}
          rowClassName='border-t border-border'
          rowProps={(t) => ({ 'data-testid': `team-row-${t.name}` })}
        />
      )}

      {showCreate && (
        <AdminModal title='New team' onClose={closeCreate}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createMut.mutate();
            }}
            className='space-y-3'
          >
            <Field label='Name'>
              <Input
                data-testid='team-name'
                value={createForm.name}
                onChange={(e) => setCreateForm((f) => ({ ...f, name: e.target.value }))}
                required
                placeholder='platform'
              />
            </Field>
            <Field
              label='Identity provider group'
              optional
              hint='Whatever your provider emits in the groups claim. Leave it empty to build the team from local users instead — you can add them once it exists.'
            >
              <Input
                data-testid='team-group'
                value={createForm.idpGroupId}
                onChange={(e) => setCreateForm((f) => ({ ...f, idpGroupId: e.target.value }))}
                mono
                placeholder='platform-engineers'
              />
            </Field>
            <AdminModalActions
              onCancel={closeCreate}
              submitLabel='Create'
              pendingLabel='Creating…'
              pending={createMut.isPending}
              error={formError(createMut.error, 'Failed to create the team')}
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
          body={`Delete "${deleteTeam.name}"? Pipelines it owns are not deleted — they become unowned, editable only by organisation admins and editors.`}
          confirmLabel='Delete'
          pendingLabel='Deleting…'
          pending={deleteMut.isPending}
          onCancel={() => {
            setDeleteTeam(null);
            deleteMut.reset();
          }}
          error={formError(deleteMut.error, 'Failed to delete the team')}
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

  const addMut = useMutation({
    mutationFn: (userId: string) => clients.team.addTeamMember({ orgId, teamId: team.id, userId }),
    onSuccess: () => {
      invalidate();
      setAddUserId('');
      toast.success('Member added');
    },
  });

  const removeMut = useMutation({
    mutationFn: (userId: string) =>
      clients.team.removeTeamMember({ orgId, teamId: team.id, userId }),
    onSuccess: () => {
      invalidate();
      toast.success('Member removed');
    },
  });

  const members = data?.items ?? [];
  const memberIds = new Set(members.map((m) => m.userId));
  // Only people in this organisation: a team is part of the org, and the
  // picker used to offer every local account on the server (#212).
  const candidates = (users?.items ?? []).filter(
    (u) => !memberIds.has(u.id) && u.orgs.some((o) => o.id === orgId),
  );

  return (
    <AdminModal title={`Members of ${team.name}`} onClose={onClose}>
      <div className='space-y-4'>
        {team.idpGroupId && (
          <Banner variant='info' testId='team-members-group-note'>
            Anyone in the group <span className='font-mono'>{team.idpGroupId}</span> is already a
            member. Those people are not listed here — membership lives in your identity provider,
            not in Shepherd. Anyone added below is a member in addition to them.
          </Banner>
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
                  {/* Named once when the display name is the login ("viewer"
                      used to read "viewerviewer"), and set apart from it
                      otherwise, the way the add picker below shows it. */}
                  {m.displayName && m.displayName !== m.login && (
                    <span className='text-xs text-muted-2'>{` — ${m.displayName}`}</span>
                  )}
                  {m.disabled && (
                    <span className={`ml-2 rounded px-1.5 py-0.5 text-2xs ${toneClass('danger')}`}>
                      disabled
                    </span>
                  )}
                </span>
                <button
                  data-testid={`team-member-remove-${m.login}`}
                  onClick={() => {
                    addMut.reset();
                    removeMut.mutate(m.userId);
                  }}
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
            removeMut.reset();
            if (addUserId) addMut.mutate(addUserId);
          }}
          className='flex items-end gap-2'
        >
          <Field label='Add a local user' className='flex-1'>
            {users ? (
              <Select
                data-testid='team-member-add-select'
                value={addUserId}
                onChange={(e) => setAddUserId(e.target.value)}
              >
                <option value=''>Select a user…</option>
                {candidates.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.login}
                    {u.displayName ? ` — ${u.displayName}` : ''}
                  </option>
                ))}
              </Select>
            ) : (
              <Input
                data-testid='team-member-add-input'
                value={addUserId}
                onChange={(e) => setAddUserId(e.target.value)}
                placeholder='user id'
                mono
              />
            )}
          </Field>
          <button
            data-testid='team-member-add'
            type='submit'
            disabled={!addUserId || addMut.isPending}
            className='rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
          >
            {addMut.isPending ? 'Adding…' : 'Add'}
          </button>
        </form>
        <FormError
          message={
            formError(addMut.error, 'Failed to add the member') ??
            formError(removeMut.error, 'Failed to remove the member')
          }
        />
      </div>
    </AdminModal>
  );
}
