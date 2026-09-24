import { useQueryClient } from '@tanstack/react-query';
import { UserPlus, UsersRound } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { Label } from '@/components/ui/label';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { selectClass } from '@/features/org/constants';
import { RoleBadge, RoleSelect } from '@/features/org/roles';
import { errorMessage } from '@/lib/api/errors';
import { useListMembers } from '@/lib/api/generated/members/members';
import { GrantRequestRole, ProjectAction, type ProjectMember } from '@/lib/api/generated/model';
import { getListProjectMembersQueryKey, grantProjectAccess, revokeProjectAccess, useListProjectMembers } from '@/lib/api/generated/projects/projects';
import { useListTeams } from '@/lib/api/generated/teams/teams';
import { cn } from '@/lib/utils';
import { canProject, useCurrentProject } from './context';

const GRANTABLE = Object.values(GrantRequestRole);
const PRINCIPAL_KINDS = ['user', 'team'] as const;
type Grantable = (typeof GRANTABLE)[number];

function GrantDialog({ projectId, projectName, existing }: { projectId: string; projectName: string; existing: Set<string> }) {
  const { t } = useTranslation(['project', 'common']);
  const org = useCurrentOrg();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<'user' | 'team'>('user');
  const [principal, setPrincipal] = useState('');
  const [role, setRole] = useState<Grantable>('developer');
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const whoId = useId();
  const roleId = useId();
  const members = useListMembers(org.id, { limit: 200 }, { query: { enabled: open } });
  const teams = useListTeams(org.id, { limit: 200 }, { query: { enabled: open } });

  const options =
    kind === 'user'
      ? (members.data?.items ?? [])
          .filter((m) => !existing.has(`user:${m.user_id}`) && m.role !== 'owner' && m.role !== 'admin')
          .map((m) => ({ value: `user:${m.user_id}`, label: `${m.display_name} (${m.email})` }))
      : (teams.data?.items ?? []).filter((tm) => !existing.has(`team:${tm.id}`)).map((tm) => ({ value: `team:${tm.id}`, label: tm.name }));
  const loading = kind === 'user' ? members.isPending : teams.isPending;

  const submit = async () => {
    if (!principal) return;
    setError(null);
    setBusy(true);
    try {
      await grantProjectAccess(projectId, principal, { role });
      toast.success(t('access.granted'));
      await queryClient.invalidateQueries({ queryKey: getListProjectMembersQueryKey(projectId) });
      setOpen(false);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setPrincipal('');
          setError(null);
        }
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <UserPlus aria-hidden />
          {t('access.add')}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('access.addTitle', { project: projectName })}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div role="radiogroup" aria-label={t('access.who')} className="inline-flex rounded-md border p-0.5">
            {PRINCIPAL_KINDS.map((k) => (
              <button
                key={k}
                type="button"
                role="radio"
                aria-checked={kind === k}
                className={cn('rounded px-3 py-1 text-sm', kind === k ? 'bg-primary text-primary-foreground' : 'text-muted-foreground')}
                onClick={() => {
                  setKind(k);
                  setPrincipal('');
                }}
              >
                {k === 'user' ? t('access.person') : t('access.team')}
              </button>
            ))}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor={whoId}>{kind === 'user' ? t('access.person') : t('access.team')}</Label>
            {loading ? (
              <LoadingState rows={1} />
            ) : options.length === 0 ? (
              <p className="text-muted-foreground text-sm">{t('access.nobody')}</p>
            ) : (
              <select
                id={whoId}
                className={cn(selectClass, 'w-full')}
                value={principal}
                onChange={(e) => {
                  setPrincipal(e.target.value);
                }}
              >
                <option value="">{t('access.choose')}</option>
                {options.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
            )}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor={roleId}>{t('access.role')}</Label>
            <select
              id={roleId}
              className={cn(selectClass, 'w-full')}
              value={role}
              onChange={(e) => {
                setRole(e.target.value as Grantable);
              }}
            >
              {GRANTABLE.map((r) => (
                <option key={r} value={r}>
                  {t(`common:roles.${r}`)}
                </option>
              ))}
            </select>
            <p className="text-muted-foreground text-xs">{t(`access.roleDescriptions.${role}`)}</p>
          </div>
          <FormError error={error} />
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => { setOpen(false); }}>
            {t('common:actions.cancel')}
          </Button>
          <Button disabled={!principal || busy} onClick={() => void submit()}>
            {t('access.add')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function SourceLabel({ m }: { m: ProjectMember }) {
  const { t } = useTranslation(['project', 'common']);
  if (m.source === 'organization') return <span>{t('access.fromOrganization', { role: t(`common:roles.${m.role}`) })}</span>;
  if (m.source === 'team') return <span>{t('access.viaTeam')}</span>;
  return <span>{t('access.direct')}</span>;
}

export function AccessPage() {
  const { t } = useTranslation(['project', 'common']);
  const project = useCurrentProject();
  const queryClient = useQueryClient();
  const members = useListProjectMembers(project.id);
  const canManage = canProject(project, ProjectAction.projectmanage_members);
  const refresh = () => queryClient.invalidateQueries({ queryKey: getListProjectMembersQueryKey(project.id) });

  const changeRole = async (m: ProjectMember, role: Grantable) => {
    try {
      await grantProjectAccess(project.id, m.principal, { role });
      toast.success(t('access.roleChanged'));
    } catch (err) {
      toast.error(errorMessage(err));
    }
    await refresh();
  };

  const existing = new Set((members.data?.items ?? []).filter((m) => m.source !== 'organization').map((m) => m.principal));

  return (
    <section className="space-y-4" aria-labelledby="access-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="max-w-2xl">
          <h2 id="access-title" className="text-lg font-semibold">
            {t('access.title')}
          </h2>
          <p className="text-muted-foreground text-sm">{t('access.description')}</p>
        </div>
        {canManage && <GrantDialog projectId={project.id} projectName={project.name} existing={existing} />}
      </div>
      {members.isPending ? (
        <LoadingState />
      ) : members.isError ? (
        <ErrorState error={members.error} onRetry={() => void members.refetch()} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="project-access">
            <TableHeader>
              <TableRow>
                <TableHead>{t('access.who')}</TableHead>
                <TableHead>{t('access.role')}</TableHead>
                <TableHead className="hidden lg:table-cell">{t('access.source')}</TableHead>
                <TableHead className="w-0" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {members.data.items.map((m) => {
                const editable = canManage && m.source !== 'organization';
                return (
                  <TableRow key={`${m.source}-${m.principal}`} data-testid={`access-${m.principal}`}>
                    <TableCell className="min-w-48 whitespace-normal">
                      <p className="flex items-center gap-1.5 font-medium">
                        {m.type === 'team' && <UsersRound aria-hidden className="text-muted-foreground size-4" />}
                        {m.name}
                      </p>
                      <p className="text-muted-foreground text-xs">
                        {m.type === 'team' ? t('access.members', { count: m.member_count ?? 0 }) : m.email}
                      </p>
                    </TableCell>
                    <TableCell>
                      {editable ? (
                        <RoleSelect
                          value={m.role}
                          roles={GRANTABLE}
                          label={t('access.roleFor', { name: m.name })}
                          onChange={(r) => void changeRole(m, r as Grantable)}
                        />
                      ) : (
                        <RoleBadge role={m.role} />
                      )}
                    </TableCell>
                    <TableCell className="text-muted-foreground hidden text-sm sm:table-cell">
                      <SourceLabel m={m} />
                    </TableCell>
                    <TableCell>
                      {editable && (
                        <ConfirmDialog
                          trigger={
                            <Button variant="outline" size="sm">
                              {t('access.remove')}
                            </Button>
                          }
                          title={t('access.removeTitle', { name: m.name })}
                          description={t('access.removeBody')}
                          confirmLabel={t('access.remove')}
                          onConfirm={async () => {
                            try {
                              await revokeProjectAccess(project.id, m.principal);
                              toast.success(t('access.removed'));
                            } catch (err) {
                              toast.error(errorMessage(err));
                            }
                            await refresh();
                          }}
                        />
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </Card>
      )}
    </section>
  );
}
