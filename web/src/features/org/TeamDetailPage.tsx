import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, Pencil, UserPlus } from 'lucide-react';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { PageHeader } from '@/components/common/PageHeader';
import { ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { useFormat } from '@/i18n/useFormat';
import { applyFieldErrors, errorMessage, hasCode } from '@/lib/api/errors';
import { useListMembers } from '@/lib/api/generated/members/members';
import type { TeamDetail } from '@/lib/api/generated/model';
import {
  addTeamMember,
  deleteTeam,
  getGetTeamQueryKey,
  getListTeamsQueryKey,
  removeTeamMember,
  updateTeam,
  useGetTeam,
} from '@/lib/api/generated/teams/teams';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { selectClass } from './constants';
import { teamSchema, type TeamValues } from './teamSchema';
import { TeamFields } from './TeamsPage';

function EditTeamDialog({ team, onSaved }: { team: TeamDetail; onSaved: () => Promise<void> }) {
  const { t } = useTranslation(['org', 'common']);
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<TeamValues>({
    resolver: zodResolver(teamSchema),
    values: { name: team.name, description: team.description },
  });

  const onSubmit = async (v: TeamValues) => {
    setError(null);
    try {
      await updateTeam(team.id, v, { headers: { 'If-Match': `"v${String(team.version)}"` } });
      toast.success(t('teams.saved'));
      setOpen(false);
    } catch (err) {
      if (!applyFieldErrors(err, form.setError, ['name', 'description'])) setError(err);
    }
    // Refresh either way: after a VERSION_CONFLICT the form shows the latest values.
    await onSaved();
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline">
          <Pencil aria-hidden />
          {t('teams.edit')}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('teams.edit')}</DialogTitle>
        </DialogHeader>
        <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <TeamFields form={form} />
          <FormError error={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { setOpen(false); }}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting ? t('common:actions.saving') : t('common:actions.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function AddMember({ orgId, team, onAdded }: { orgId: string; team: TeamDetail; onAdded: () => Promise<void> }) {
  const { t } = useTranslation('org');
  const members = useListMembers(orgId, { limit: 200 });
  const [userId, setUserId] = useState('');
  const [busy, setBusy] = useState(false);
  const inTeam = new Set(team.members.map((m) => m.user_id));
  const candidates = (members.data?.items ?? []).filter((m) => !inTeam.has(m.user_id));

  if (members.isPending) return <LoadingState rows={1} />;
  if (members.isError) return <ErrorState error={members.error} onRetry={() => void members.refetch()} />;
  if (candidates.length === 0) return <p className="text-muted-foreground text-sm">{t('teams.noCandidates')}</p>;

  const add = async () => {
    if (!userId) return;
    setBusy(true);
    try {
      await addTeamMember(team.id, userId);
      toast.success(t('teams.added'));
      setUserId('');
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setBusy(false);
    }
    await onAdded();
  };

  return (
    <div className="flex flex-wrap gap-2">
      <select
        aria-label={t('teams.selectMember')}
        className={`${selectClass} min-w-0 flex-1 sm:max-w-sm`}
        value={userId}
        onChange={(e) => {
          setUserId(e.target.value);
        }}
      >
        <option value="">{t('teams.selectMember')}</option>
        {candidates.map((m) => (
          <option key={m.user_id} value={m.user_id}>
            {m.display_name} ({m.email})
          </option>
        ))}
      </select>
      <Button onClick={() => void add()} disabled={!userId || busy}>
        <UserPlus aria-hidden />
        {t('teams.addMember')}
      </Button>
    </div>
  );
}

export function TeamDetailPage() {
  const { t } = useTranslation(['org', 'common']);
  const { teamId = '' } = useParams();
  const org = useCurrentOrg();
  const fmt = useFormat();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const perms = usePermissions(org.id);
  const team = useGetTeam(teamId, { query: { retry: (n, err) => !hasCode(err, 'TEAM_NOT_FOUND') && n < 2 } });
  const canManage = perms.can(OrgAction.teamManage);

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: getGetTeamQueryKey(teamId) });
    await queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey(org.id) });
  };

  if (team.isPending) return <LoadingState />;
  if (team.isError) {
    if (hasCode(team.error, 'TEAM_NOT_FOUND')) return <NotFoundPage />;
    return <ErrorState error={team.error} onRetry={() => void team.refetch()} />;
  }
  const data = team.data;

  return (
    <div className="space-y-6">
      <Link to={`/o/${org.slug}/teams`} className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm">
        <ArrowLeft aria-hidden className="size-4 rtl:rotate-180" />
        {t('teams.back')}
      </Link>
      <PageHeader
        title={data.name}
        description={data.description || undefined}
        actions={
          canManage ? (
            <div className="flex flex-wrap gap-2">
              <EditTeamDialog team={data} onSaved={refresh} />
              <ConfirmDialog
                trigger={<Button variant="destructive">{t('teams.delete')}</Button>}
                title={t('teams.deleteTitle', { team: data.name })}
                description={t('teams.deleteBody')}
                confirmLabel={t('teams.delete')}
                onConfirm={async () => {
                  try {
                    await deleteTeam(data.id);
                    toast.success(t('teams.deleted'));
                    queryClient.removeQueries({ queryKey: getGetTeamQueryKey(data.id) });
                    await queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey(org.id) });
                    await navigate(`/o/${org.slug}/teams`, { replace: true });
                  } catch (err) {
                    toast.error(errorMessage(err));
                  }
                }}
              />
            </div>
          ) : undefined
        }
      />

      <Card>
        <CardHeader>
          <CardTitle>{t('teams.members')}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {canManage && <AddMember orgId={org.id} team={data} onAdded={refresh} />}
          {data.members.length === 0 ? (
            <p className="text-muted-foreground text-sm">{t('teams.noMembers')}</p>
          ) : (
            <ul className="divide-y" data-testid="team-members">
              {data.members.map((m) => (
                <li key={m.user_id} className="flex flex-wrap items-center gap-3 py-3">
                  <div className="min-w-0 flex-1">
                    <p className="truncate font-medium">{m.display_name}</p>
                    <p className="text-muted-foreground truncate text-xs">
                      {m.email} · {fmt.dateTime(m.added_at, { dateStyle: 'medium' })}
                    </p>
                  </div>
                  {canManage && (
                    <ConfirmDialog
                      trigger={
                        <Button variant="outline" size="sm">
                          {t('teams.removeMember')}
                        </Button>
                      }
                      title={t('teams.removeTitle', { name: m.display_name, team: data.name })}
                      description={t('teams.removeBody')}
                      confirmLabel={t('teams.removeMember')}
                      onConfirm={async () => {
                        try {
                          await removeTeamMember(data.id, m.user_id);
                          toast.success(t('teams.removed'));
                        } catch (err) {
                          toast.error(errorMessage(err));
                        }
                        await refresh();
                      }}
                    />
                  )}
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
