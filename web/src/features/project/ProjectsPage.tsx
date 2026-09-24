import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { FolderGit2, GitBranch, Plus, Search } from 'lucide-react';
import { useDeferredValue, useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { toast } from 'sonner';
import { z } from 'zod';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { TextField } from '@/components/common/Field';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { SLUG_PATTERN, slugify } from '@/features/org/slug';
import { applyFieldErrors, errorMessage, hasCode } from '@/lib/api/errors';
import { createProject, getListProjectsQueryKey, useListProjects } from '@/lib/api/generated/projects/projects';
import { idempotencyHeaders, newIdempotencyKey } from '@/lib/api/idempotency';

const schema = z.object({
  name: z.string().trim().min(1, 'errors:rules.required').max(100, 'errors:rules.max'),
  slug: z.union([z.literal(''), z.string().regex(SLUG_PATTERN, 'errors:rules.slug')]),
  description: z.string().trim().max(500, 'errors:rules.max'),
  default_branch: z.string().trim().min(1, 'errors:rules.required').max(255, 'errors:rules.max').regex(/^[A-Za-z0-9._/-]+$/, 'errors:rules.branch'),
});
type Values = z.infer<typeof schema>;
const MAX: Record<keyof Values, number> = { name: 100, slug: 40, description: 500, default_branch: 255 };

function CreateProjectDialog({ orgId, orgSlug }: { orgId: string; orgSlug: string }) {
  const { t } = useTranslation(['project', 'common', 'errors']);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [slugEdited, setSlugEdited] = useState(false);
  const [key, setKey] = useState(newIdempotencyKey);
  const defaults: Values = { name: '', slug: '', description: '', default_branch: 'main' };
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: defaults });
  const errs = form.formState.errors;
  const msg = (field: keyof Values) => {
    const e = errs[field];
    if (!e?.message) return undefined;
    return e.type === 'server' ? e.message : t(e.message as 'errors:rules.max', { param: MAX[field] });
  };

  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) {
      form.reset(defaults);
      setSlugEdited(false);
      setError(null);
      setKey(newIdempotencyKey());
    }
  };

  const onSubmit = async (v: Values) => {
    setError(null);
    try {
      const p = await createProject(orgId, v, idempotencyHeaders(key));
      await queryClient.invalidateQueries({ queryKey: getListProjectsQueryKey(orgId) });
      toast.success(t('create.created'));
      setOpen(false);
      await navigate(`/o/${orgSlug}/projects/${p.id}`);
    } catch (err) {
      if (hasCode(err, 'SLUG_TAKEN')) {
        form.setError('slug', { type: 'server', message: errorMessage(err) });
        return;
      }
      if (!applyFieldErrors(err, form.setError, ['name', 'slug', 'description', 'default_branch'])) setError(err);
    }
  };

  const nameField = form.register('name');
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button>
          <Plus aria-hidden />
          {t('list.create')}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('create.title')}</DialogTitle>
        </DialogHeader>
        <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <TextField
            label={t('create.name')}
            autoComplete="off"
            error={msg('name')}
            {...nameField}
            onChange={(e) => {
              void nameField.onChange(e);
              if (!slugEdited) form.setValue('slug', slugify(e.target.value), { shouldValidate: form.formState.isSubmitted });
            }}
          />
          <TextField
            label={t('create.slug')}
            hint={t('create.slugHint')}
            autoComplete="off"
            spellCheck={false}
            error={msg('slug')}
            {...form.register('slug', {
              onChange: () => {
                setSlugEdited(true);
              },
            })}
          />
          <TextField label={t('create.description')} autoComplete="off" error={msg('description')} {...form.register('description')} />
          <TextField label={t('create.defaultBranch')} autoComplete="off" spellCheck={false} error={msg('default_branch')} {...form.register('default_branch')} />
          <FormError error={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { setOpen(false); }}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {t('create.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function ProjectsPage() {
  const { t } = useTranslation('project');
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const [search, setSearch] = useState('');
  const q = useDeferredValue(search.trim());
  const projects = useListProjects(org.id, { limit: 200, ...(q ? { q } : {}) });
  const canCreate = perms.can(OrgAction.projectCreate);

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('list.title')}
        description={t('list.description', { org: org.name })}
        actions={canCreate ? <CreateProjectDialog orgId={org.id} orgSlug={org.slug} /> : undefined}
      />
      <div className="relative sm:max-w-xs">
        <Search aria-hidden className="text-muted-foreground absolute start-3 top-1/2 size-4 -translate-y-1/2" />
        <Input
          type="search"
          aria-label={t('list.search')}
          placeholder={t('list.search')}
          className="ps-9"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
          }}
        />
      </div>
      {projects.isPending ? (
        <LoadingState />
      ) : projects.isError ? (
        <ErrorState error={projects.error} onRetry={() => void projects.refetch()} />
      ) : projects.data.items.length === 0 ? (
        q ? (
          <EmptyState title={t('list.noMatch')} />
        ) : (
          <EmptyState title={t('list.empty')} description={canCreate ? t('list.emptyDescription') : t('list.noAccess')} />
        )
      ) : (
        <ul className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3" data-testid="project-list">
          {projects.data.items.map((p) => (
            <li key={p.id}>
              <Link
                to={`/o/${org.slug}/projects/${p.id}`}
                className="focus-visible:ring-ring/50 block h-full rounded-xl outline-none focus-visible:ring-[3px]"
              >
                <Card className="hover:bg-accent/40 h-full transition-colors">
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2">
                      <FolderGit2 aria-hidden className="text-muted-foreground size-4 shrink-0" />
                      <span className="min-w-0 break-words">{p.name}</span>
                    </CardTitle>
                    {p.description && <CardDescription className="break-words">{p.description}</CardDescription>}
                  </CardHeader>
                  <CardContent className="text-muted-foreground flex items-center gap-1 text-sm">
                    <GitBranch aria-hidden className="size-4" />
                    <span className="truncate">{t('list.defaultBranch', { branch: p.default_branch })}</span>
                  </CardContent>
                </Card>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
