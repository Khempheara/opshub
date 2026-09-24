import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { useId, useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { toast } from 'sonner';
import { z } from 'zod';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { useSession } from '@/auth/session';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { TextField } from '@/components/common/Field';
import { PageHeader } from '@/components/common/PageHeader';
import { ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { applyFieldErrors, errorMessage } from '@/lib/api/errors';
import { getListMembersQueryKey, removeMember, useListMembers } from '@/lib/api/generated/members/members';
import type { Organization } from '@/lib/api/generated/model';
import {
  deleteOrganization,
  getGetOrganizationPermissionsQueryKey,
  getGetOrganizationQueryKey,
  getListOrganizationsQueryKey,
  transferOwnership,
  updateOrganization,
  useGetOrganization,
} from '@/lib/api/generated/organizations/organizations';
import { selectClass } from './constants';

const nameSchema = z.object({ name: z.string().trim().min(1, 'errors:rules.required').max(100, 'errors:rules.max') });

function RenameCard({ org }: { org: Organization }) {
  const { t } = useTranslation(['org', 'common', 'errors']);
  const queryClient = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const form = useForm<{ name: string }>({ resolver: zodResolver(nameSchema), values: { name: org.name } });
  const nameError = form.formState.errors.name?.message;

  const onSubmit = async (v: { name: string }) => {
    setError(null);
    try {
      await updateOrganization(org.id, v, { headers: { 'If-Match': `"v${String(org.version)}"` } });
      toast.success(t('general.renamed'));
    } catch (err) {
      if (!applyFieldErrors(err, form.setError, ['name'])) setError(err);
    }
    // After a VERSION_CONFLICT this loads the latest name and version.
    await queryClient.invalidateQueries({ queryKey: getGetOrganizationQueryKey(org.id) });
    await queryClient.invalidateQueries({ queryKey: getListOrganizationsQueryKey() });
  };

  return (
    <Card>
      <form noValidate onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
        <CardHeader>
          <CardTitle>{t('general.nameTitle')}</CardTitle>
          <CardDescription>{t('general.nameDescription', { slug: org.slug })}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4 pt-4">
          <TextField
            label={t('general.nameTitle')}
            autoComplete="organization"
            error={nameError && (nameError.startsWith('errors:') ? t(nameError as 'errors:rules.max', { param: 100 }) : nameError)}
            {...form.register('name')}
          />
          <FormError error={error} />
        </CardContent>
        <CardFooter className="pt-4">
          <Button type="submit" disabled={form.formState.isSubmitting || !form.formState.isDirty}>
            {form.formState.isSubmitting ? t('common:actions.saving') : t('common:actions.save')}
          </Button>
        </CardFooter>
      </form>
    </Card>
  );
}

function TransferCard({ org, myId }: { org: Organization; myId: string }) {
  const { t } = useTranslation('org');
  const queryClient = useQueryClient();
  const members = useListMembers(org.id, { limit: 200 });
  const [userId, setUserId] = useState('');
  const selectId = useId();
  const candidates = (members.data?.items ?? []).filter((m) => m.user_id !== myId && m.role !== 'owner');
  const target = candidates.find((m) => m.user_id === userId);

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('general.transferTitle')}</CardTitle>
        <CardDescription>{t('general.transferDescription')}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 pt-4">
        {members.isPending ? (
          <LoadingState rows={1} />
        ) : members.isError ? (
          <ErrorState error={members.error} onRetry={() => void members.refetch()} />
        ) : candidates.length === 0 ? (
          <p className="text-muted-foreground text-sm">{t('general.noTransferCandidates')}</p>
        ) : (
          <div className="flex flex-wrap items-end gap-2">
            <div className="min-w-0 flex-1 space-y-1.5 sm:max-w-sm">
              <Label htmlFor={selectId}>{t('general.transferTo')}</Label>
              <select
                id={selectId}
                className={`${selectClass} w-full`}
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
            </div>
            <ConfirmDialog
              trigger={
                <Button variant="outline" disabled={!target}>
                  {t('general.transfer')}
                </Button>
              }
              title={t('general.transferConfirmTitle', { name: target?.display_name ?? '' })}
              description={t('general.transferConfirmBody')}
              confirmLabel={t('general.transfer')}
              onConfirm={async () => {
                if (!target) return;
                try {
                  await transferOwnership(org.id, { user_id: target.user_id });
                  toast.success(t('general.transferred'));
                  setUserId('');
                } catch (err) {
                  toast.error(errorMessage(err));
                }
                await queryClient.invalidateQueries({ queryKey: getGetOrganizationPermissionsQueryKey(org.id) });
                await queryClient.invalidateQueries({ queryKey: getListMembersQueryKey(org.id) });
                await queryClient.invalidateQueries({ queryKey: getListOrganizationsQueryKey() });
              }}
            />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function LeaveCard({ org, myId }: { org: Organization; myId: string }) {
  const { t } = useTranslation('org');
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('general.leaveTitle')}</CardTitle>
        <CardDescription>{t('general.leaveDescription', { org: org.name })}</CardDescription>
      </CardHeader>
      <CardFooter className="pt-4">
        <ConfirmDialog
          trigger={<Button variant="outline">{t('general.leave')}</Button>}
          title={t('general.leaveConfirmTitle', { org: org.name })}
          description={t('general.leaveDescription', { org: org.name })}
          confirmLabel={t('general.leave')}
          onConfirm={async () => {
            try {
              await removeMember(org.id, myId);
              toast.success(t('general.left', { org: org.name }));
              await queryClient.invalidateQueries({ queryKey: getListOrganizationsQueryKey() });
              await navigate('/', { replace: true });
            } catch (err) {
              toast.error(errorMessage(err));
            }
          }}
        />
      </CardFooter>
    </Card>
  );
}

function DeleteCard({ org }: { org: Organization }) {
  const { t } = useTranslation('org');
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const inputId = useId();

  const onDelete = async () => {
    setError(null);
    setBusy(true);
    try {
      await deleteOrganization(org.id, { confirm });
      toast.success(t('general.deleted'));
      queryClient.removeQueries({ queryKey: getGetOrganizationQueryKey(org.id) });
      await queryClient.invalidateQueries({ queryKey: getListOrganizationsQueryKey() });
      await navigate('/', { replace: true });
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <Card className="border-destructive/40">
      <CardHeader>
        <CardTitle className="text-destructive">{t('general.deleteTitle')}</CardTitle>
        <CardDescription>{t('general.deleteDescription', { org: org.name })}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 pt-4">
        <div className="space-y-1.5 sm:max-w-sm">
          <Label htmlFor={inputId}>{t('general.deleteConfirmLabel', { slug: org.slug })}</Label>
          <Input
            id={inputId}
            autoComplete="off"
            spellCheck={false}
            value={confirm}
            onChange={(e) => {
              setConfirm(e.target.value);
            }}
          />
        </div>
        <FormError error={error} />
      </CardContent>
      <CardFooter className="pt-4">
        <Button variant="destructive" disabled={confirm !== org.slug || busy} onClick={() => void onDelete()}>
          {t('general.delete')}
        </Button>
      </CardFooter>
    </Card>
  );
}

export function OrgSettingsPage() {
  const { t } = useTranslation('org');
  const current = useCurrentOrg();
  const { user } = useSession();
  const perms = usePermissions(current.id);
  // The list response has version 0; the detail carries the version needed for If-Match.
  const org = useGetOrganization(current.id);
  const myId = user?.id ?? '';

  if (org.isPending || perms.isPending) return <LoadingState />;
  if (org.isError) return <ErrorState error={org.error} onRetry={() => void org.refetch()} />;

  return (
    <div className="max-w-3xl space-y-6">
      <PageHeader title={t('general.title')} />
      {perms.can(OrgAction.orgUpdate) && <RenameCard org={org.data} />}
      {perms.can(OrgAction.orgTransfer) && <TransferCard org={org.data} myId={myId} />}
      <LeaveCard org={org.data} myId={myId} />
      {perms.can(OrgAction.orgDelete) && <DeleteCard org={org.data} />}
    </div>
  );
}
