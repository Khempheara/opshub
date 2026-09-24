import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { useId, useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { toast } from 'sonner';
import { z } from 'zod';
import { useCurrentOrg } from '@/app/org';
import { TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { applyFieldErrors } from '@/lib/api/errors';
import { ProjectAction, type ProjectDetail } from '@/lib/api/generated/model';
import { deleteProject, getGetProjectQueryKey, getListProjectsQueryKey, updateProject } from '@/lib/api/generated/projects/projects';
import { ifMatch } from '@/lib/api/idempotency';
import { canProject, useCurrentProject } from './context';

const schema = z.object({
  name: z.string().trim().min(1, 'errors:rules.required').max(100, 'errors:rules.max'),
  description: z.string().trim().max(500, 'errors:rules.max'),
  default_branch: z.string().trim().min(1, 'errors:rules.required').max(255, 'errors:rules.max').regex(/^[A-Za-z0-9._/-]+$/, 'errors:rules.branch'),
});
type Values = z.infer<typeof schema>;
const MAX: Record<keyof Values, number> = { name: 100, description: 500, default_branch: 255 };

function GeneralCard({ project }: { project: ProjectDetail }) {
  const { t } = useTranslation(['project', 'common', 'errors']);
  const queryClient = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const editable = canProject(project, ProjectAction.projectupdate);
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    values: { name: project.name, description: project.description, default_branch: project.default_branch },
  });
  const errs = form.formState.errors;
  const msg = (field: keyof Values) => {
    const e = errs[field];
    if (!e?.message) return undefined;
    return e.type === 'server' ? e.message : t(e.message as 'errors:rules.max', { param: MAX[field] });
  };

  const onSubmit = async (v: Values) => {
    setError(null);
    try {
      await updateProject(project.id, v, ifMatch(project.version));
      toast.success(t('settings.saved'));
    } catch (err) {
      if (!applyFieldErrors(err, form.setError, ['name', 'description', 'default_branch'])) setError(err);
    }
    // After a VERSION_CONFLICT this loads the latest values and version.
    await queryClient.invalidateQueries({ queryKey: getGetProjectQueryKey(project.id) });
    await queryClient.invalidateQueries({ queryKey: getListProjectsQueryKey(project.organization_id) });
  };

  return (
    <Card>
      <form noValidate onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
        <CardHeader>
          <CardTitle>{t('settings.general')}</CardTitle>
          {!editable && <CardDescription>{t('settings.readOnly')}</CardDescription>}
        </CardHeader>
        <CardContent className="space-y-4 pt-4">
          <fieldset disabled={!editable} className="space-y-4">
            <TextField label={t('create.name')} autoComplete="off" error={msg('name')} {...form.register('name')} />
            <TextField label={t('create.description')} autoComplete="off" error={msg('description')} {...form.register('description')} />
            <TextField label={t('create.defaultBranch')} autoComplete="off" spellCheck={false} error={msg('default_branch')} {...form.register('default_branch')} />
          </fieldset>
          <FormError error={error} />
        </CardContent>
        {editable && (
          <CardFooter className="pt-4">
            <Button type="submit" disabled={form.formState.isSubmitting || !form.formState.isDirty}>
              {form.formState.isSubmitting ? t('common:actions.saving') : t('common:actions.save')}
            </Button>
          </CardFooter>
        )}
      </form>
    </Card>
  );
}

function DeleteCard({ project }: { project: ProjectDetail }) {
  const { t } = useTranslation('project');
  const org = useCurrentOrg();
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
      await deleteProject(project.id, { confirm });
      toast.success(t('settings.deleted'));
      queryClient.removeQueries({ queryKey: getGetProjectQueryKey(project.id) });
      await queryClient.invalidateQueries({ queryKey: getListProjectsQueryKey(project.organization_id) });
      await navigate(`/o/${org.slug}/projects`, { replace: true });
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <Card className="border-destructive/40">
      <CardHeader>
        <CardTitle className="text-destructive">{t('settings.deleteTitle')}</CardTitle>
        <CardDescription>{t('settings.deleteDescription', { project: project.name })}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 pt-4">
        <div className="space-y-1.5 sm:max-w-sm">
          <Label htmlFor={inputId}>{t('settings.deleteConfirmLabel', { slug: project.slug })}</Label>
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
        <Button variant="destructive" disabled={confirm !== project.slug || busy} onClick={() => void onDelete()}>
          {t('settings.delete')}
        </Button>
      </CardFooter>
    </Card>
  );
}

export function ProjectSettingsPage() {
  const project = useCurrentProject();
  return (
    <div className="max-w-3xl space-y-6">
      <GeneralCard project={project} />
      {canProject(project, ProjectAction.projectdelete) && <DeleteCard project={project} />}
    </div>
  );
}
