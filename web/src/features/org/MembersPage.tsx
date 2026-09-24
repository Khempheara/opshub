import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { MailPlus, Search, ShieldCheck } from 'lucide-react';
import { useDeferredValue, useState } from 'react';
import { useForm, useWatch } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { toast } from 'sonner';
import { z } from 'zod';
import { useCurrentOrg } from '@/app/org';
import { assignableRoles, canManageMember, OrgAction, usePermissions } from '@/app/permissions';
import { useSession } from '@/auth/session';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { Field, TextField } from '@/components/common/Field';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage, hasCode } from '@/lib/api/errors';
import {
  getListInvitationsQueryKey,
  getListMembersQueryKey,
  inviteMember,
  removeMember,
  revokeInvitation,
  updateMemberRole,
  useListInvitations,
  useListMembers,
} from '@/lib/api/generated/members/members';
import { getGetOrganizationPermissionsQueryKey, getListOrganizationsQueryKey } from '@/lib/api/generated/organizations/organizations';
import type { Member, Role } from '@/lib/api/generated/model';
import { ALL_ROLES, selectClass } from './constants';
import { RoleBadge, RoleSelect } from './roles';

const inviteSchema = z.object({
  email: z.string().trim().min(1, 'errors:rules.required').pipe(z.email('errors:rules.email')),
  role: z.enum(['owner', 'admin', 'developer', 'viewer']),
});
type InviteValues = z.infer<typeof inviteSchema>;

function InviteDialog({ orgId, orgName, roles }: { orgId: string; orgName: string; roles: Role[] }) {
  const { t } = useTranslation(['org', 'common', 'errors']);
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<InviteValues>({ resolver: zodResolver(inviteSchema), defaultValues: { email: '', role: 'developer' } });
  const emailError = form.formState.errors.email?.message;
  const role = useWatch({ control: form.control, name: 'role' });

  const onSubmit = async (v: InviteValues) => {
    setError(null);
    try {
      await inviteMember(orgId, v);
      toast.success(t('invitations.sent', { email: v.email }));
      await queryClient.invalidateQueries({ queryKey: getListInvitationsQueryKey(orgId) });
      form.reset();
      setOpen(false);
    } catch (err) {
      if (hasCode(err, 'ALREADY_MEMBER')) form.setError('email', { type: 'server', message: errorMessage(err) });
      else setError(err);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <MailPlus aria-hidden />
          {t('members.invite')}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('invitations.dialogTitle', { org: orgName })}</DialogTitle>
          <DialogDescription>{t('invitations.dialogBody')}</DialogDescription>
        </DialogHeader>
        <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <TextField
            label={t('invitations.email')}
            type="email"
            autoComplete="off"
            error={emailError && (form.formState.errors.email?.type === 'server' ? emailError : t(emailError as 'errors:rules.email'))}
            {...form.register('email')}
          />
          <Field label={t('invitations.role')} hint={t(`roleDescriptions.${role}`)}>
            {({ id, describedBy }) => (
              <select id={id} aria-describedby={describedBy} className={`${selectClass} w-full`} {...form.register('role')}>
                {roles.map((r) => (
                  <option key={r} value={r}>
                    {t(`common:roles.${r}`)}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <FormError error={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { setOpen(false); }}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {t('invitations.send')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function PendingInvitations({ orgId }: { orgId: string }) {
  const { t } = useTranslation(['org', 'common']);
  const fmt = useFormat();
  const queryClient = useQueryClient();
  const invitations = useListInvitations(orgId, { limit: 100 });

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('invitations.title')}</CardTitle>
      </CardHeader>
      <CardContent>
        {invitations.isPending ? (
          <LoadingState rows={2} />
        ) : invitations.isError ? (
          <ErrorState error={invitations.error} onRetry={() => void invitations.refetch()} />
        ) : invitations.data.items.length === 0 ? (
          <p className="text-muted-foreground text-sm">{t('invitations.empty')}</p>
        ) : (
          <ul className="divide-y" data-testid="pending-invitations">
            {invitations.data.items.map((inv) => (
              <li key={inv.id} className="flex flex-wrap items-center gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium">{inv.email}</p>
                  <p className="text-muted-foreground text-xs">
                    {inv.invited_by_name && `${t('invitations.invitedBy', { name: inv.invited_by_name })} · `}
                    {t('invitations.expires', { time: fmt.relative(inv.expires_at) })}
                  </p>
                </div>
                <RoleBadge role={inv.role} />
                <ConfirmDialog
                  trigger={
                    <Button variant="outline" size="sm">
                      {t('invitations.revoke')}
                    </Button>
                  }
                  title={t('invitations.revokeTitle', { email: inv.email })}
                  description={t('invitations.revokeBody')}
                  confirmLabel={t('invitations.revoke')}
                  onConfirm={async () => {
                    try {
                      await revokeInvitation(inv.id);
                      toast.success(t('invitations.revoked'));
                    } catch (err) {
                      toast.error(errorMessage(err));
                    }
                    await queryClient.invalidateQueries({ queryKey: getListInvitationsQueryKey(orgId) });
                  }}
                />
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

export function MembersPage() {
  const { t } = useTranslation(['org', 'common']);
  const org = useCurrentOrg();
  const { user } = useSession();
  const fmt = useFormat();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const perms = usePermissions(org.id);
  const [search, setSearch] = useState('');
  const [roleFilter, setRoleFilter] = useState<Role | ''>('');
  const deferredSearch = useDeferredValue(search.trim());
  const members = useListMembers(org.id, {
    limit: 200,
    ...(deferredSearch ? { q: deferredSearch } : {}),
    ...(roleFilter ? { role: roleFilter } : {}),
  });
  const myRole = perms.role ?? org.role;
  const myId = user?.id ?? '';

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: getListMembersQueryKey(org.id) });
    await queryClient.invalidateQueries({ queryKey: getGetOrganizationPermissionsQueryKey(org.id) });
  };

  const changeRole = async (m: Member, role: Role) => {
    try {
      await updateMemberRole(org.id, m.user_id, { role });
      toast.success(t('members.roleChanged'));
    } catch (err) {
      toast.error(errorMessage(err));
    }
    await refresh();
  };

  const remove = async (m: Member) => {
    try {
      await removeMember(org.id, m.user_id);
      if (m.user_id === myId) {
        await queryClient.invalidateQueries({ queryKey: getListOrganizationsQueryKey() });
        toast.success(t('general.left', { org: org.name }));
        await navigate('/', { replace: true });
        return;
      }
      toast.success(t('members.removed'));
    } catch (err) {
      toast.error(errorMessage(err));
    }
    await refresh();
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('members.title')}
        description={t('members.description', { org: org.name })}
        actions={perms.can(OrgAction.memberInvite) ? <InviteDialog orgId={org.id} orgName={org.name} roles={assignableRoles(myRole)} /> : undefined}
      />

      <div className="flex flex-wrap gap-2">
        <div className="relative min-w-0 flex-1 sm:max-w-xs">
          <Search aria-hidden className="text-muted-foreground absolute start-3 top-1/2 size-4 -translate-y-1/2" />
          <Input
            type="search"
            aria-label={t('members.search')}
            placeholder={t('members.search')}
            className="ps-9"
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
            }}
          />
        </div>
        <select
          aria-label={t('members.columns.role')}
          className={selectClass}
          value={roleFilter}
          onChange={(e) => {
            setRoleFilter(e.target.value as Role | '');
          }}
        >
          <option value="">{t('members.allRoles')}</option>
          {ALL_ROLES.map((r) => (
            <option key={r} value={r}>
              {t(`common:roles.${r}`)}
            </option>
          ))}
        </select>
      </div>

      {members.isPending ? (
        <LoadingState />
      ) : members.isError ? (
        <ErrorState error={members.error} onRetry={() => void members.refetch()} />
      ) : members.data.items.length === 0 ? (
        <EmptyState title={t('members.empty')} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="members-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('members.columns.member')}</TableHead>
                <TableHead>{t('members.columns.role')}</TableHead>
                <TableHead className="hidden lg:table-cell">{t('members.columns.twoFactor')}</TableHead>
                <TableHead className="hidden xl:table-cell">{t('members.columns.joined')}</TableHead>
                <TableHead className="w-0" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {members.data.items.map((m) => {
                const self = m.user_id === myId;
                const manageable = perms.can(OrgAction.memberUpdateRole) && canManageMember(myRole, myId, m);
                const roles = assignableRoles(myRole).includes(m.role) ? assignableRoles(myRole) : [m.role, ...assignableRoles(myRole)];
                return (
                  <TableRow key={m.user_id} data-testid={`member-${m.email}`}>
                    <TableCell className="min-w-48 whitespace-normal">
                      <p className="font-medium">
                        {m.display_name}
                        {self && <span className="text-muted-foreground ms-2 text-xs">({t('members.you')})</span>}
                      </p>
                      <p className="text-muted-foreground text-xs">{m.email}</p>
                    </TableCell>
                    <TableCell>
                      {manageable ? (
                        <RoleSelect
                          value={m.role}
                          roles={roles}
                          label={t('members.roleFor', { name: m.display_name })}
                          onChange={(role) => void changeRole(m, role)}
                        />
                      ) : (
                        <RoleBadge role={m.role} />
                      )}
                    </TableCell>
                    <TableCell className="hidden lg:table-cell">
                      {m.two_factor_enabled ? (
                        <span className="text-success inline-flex items-center gap-1 text-sm">
                          <ShieldCheck aria-hidden className="size-4" />
                          {t('members.twoFactorOn')}
                        </span>
                      ) : (
                        <span className="text-muted-foreground text-sm">{t('members.twoFactorOff')}</span>
                      )}
                    </TableCell>
                    <TableCell className="hidden text-sm xl:table-cell">{fmt.dateTime(m.joined_at, { dateStyle: 'medium' })}</TableCell>
                    <TableCell>
                      {(self || (perms.can(OrgAction.memberRemove) && canManageMember(myRole, myId, m))) && (
                        <ConfirmDialog
                          trigger={
                            <Button variant="outline" size="sm">
                              {self ? t('general.leave') : t('members.remove')}
                            </Button>
                          }
                          title={self ? t('general.leaveConfirmTitle', { org: org.name }) : t('members.removeTitle', { name: m.display_name })}
                          description={self ? t('general.leaveDescription', { org: org.name }) : t('members.removeBody', { org: org.name })}
                          confirmLabel={self ? t('general.leave') : t('members.remove')}
                          onConfirm={() => remove(m)}
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

      {perms.can(OrgAction.memberInvite) && <PendingInvitations orgId={org.id} />}
    </div>
  );
}
