import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { Plus, UsersRound } from 'lucide-react';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { TextField } from '@/components/common/Field';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { applyFieldErrors } from '@/lib/api/errors';
import { createTeam, getListTeamsQueryKey, useListTeams } from '@/lib/api/generated/teams/teams';
import { teamSchema, type TeamValues } from './teamSchema';

function CreateTeamDialog({ orgId, orgSlug }: { orgId: string; orgSlug: string }) {
  const { t } = useTranslation(['org', 'common', 'errors']);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<TeamValues>({ resolver: zodResolver(teamSchema), defaultValues: { name: '', description: '' } });

  const onSubmit = async (v: TeamValues) => {
    setError(null);
    try {
      const team = await createTeam(orgId, v);
      await queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey(orgId) });
      toast.success(t('teams.created'));
      setOpen(false);
      form.reset();
      await navigate(`/o/${orgSlug}/teams/${team.id}`);
    } catch (err) {
      if (!applyFieldErrors(err, form.setError, ['name', 'description'])) setError(err);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <Plus aria-hidden />
          {t('teams.create')}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('teams.createTitle')}</DialogTitle>
        </DialogHeader>
        <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <TeamFields form={form} />
          <FormError error={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { setOpen(false); }}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {t('teams.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Name + description inputs shared by the create and edit forms. */
export function TeamFields({ form }: { form: ReturnType<typeof useForm<TeamValues>> }) {
  const { t } = useTranslation(['org', 'errors']);
  const errs = form.formState.errors;
  // Client-side messages are i18n keys; server field errors arrive already translated.
  const msg = (m: string | undefined, max: number) => m && (m.startsWith('errors:') ? t(m as 'errors:rules.max', { param: max }) : m);
  return (
    <>
      <TextField label={t('teams.name')} autoComplete="off" error={msg(errs.name?.message, 100)} {...form.register('name')} />
      <TextField label={t('teams.descriptionLabel')} autoComplete="off" error={msg(errs.description?.message, 500)} {...form.register('description')} />
    </>
  );
}

export function TeamsPage() {
  const { t } = useTranslation('org');
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const teams = useListTeams(org.id, { limit: 200 });
  const canManage = perms.can(OrgAction.teamManage);

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('teams.title')}
        description={t('teams.description')}
        actions={canManage ? <CreateTeamDialog orgId={org.id} orgSlug={org.slug} /> : undefined}
      />
      {teams.isPending ? (
        <LoadingState />
      ) : teams.isError ? (
        <ErrorState error={teams.error} onRetry={() => void teams.refetch()} />
      ) : teams.data.items.length === 0 ? (
        <EmptyState title={t('teams.empty')} />
      ) : (
        <ul className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {teams.data.items.map((team) => (
            <li key={team.id}>
              <Link
                to={`/o/${org.slug}/teams/${team.id}`}
                className="focus-visible:ring-ring/50 block h-full rounded-xl outline-none focus-visible:ring-[3px]"
              >
                <Card className="hover:bg-accent/40 h-full transition-colors">
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2">
                      <UsersRound aria-hidden className="text-muted-foreground size-4 shrink-0" />
                      <span className="min-w-0 break-words">{team.name}</span>
                    </CardTitle>
                    {team.description && <CardDescription className="break-words">{team.description}</CardDescription>}
                  </CardHeader>
                  <CardContent className="text-muted-foreground text-sm">{t('teams.memberCount', { count: team.member_count })}</CardContent>
                </Card>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
