import {
  Activity,
  Boxes,
  Building2,
  ChevronDown,
  ChevronRight,
  FolderGit2,
  History,
  KeyRound,
  Rocket,
  ScrollText,
  Server,
  ShieldCheck,
  Workflow,
  type LucideIcon,
} from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useFormat } from '@/i18n/useFormat';
import type { AuditEntry } from '@/lib/api/generated/model';
import { cn } from '@/lib/utils';
import { categoryOf, changes, device, metadataEntries, type Category } from './auditView';

const ICONS: Record<Category, LucideIcon> = {
  organization: Building2,
  projects: FolderGit2,
  pipelines: Workflow,
  runners: Server,
  deployments: Rocket,
  infrastructure: Boxes,
  secrets: KeyRound,
  monitoring: Activity,
  logs: ScrollText,
  audit: History,
};

const DATE_TIME: Intl.DateTimeFormatOptions = { dateStyle: 'medium', timeStyle: 'medium' };

/**
 * One audit entry: its sentence, when, and from where. Details open below it: the action,
 * the actor, the resource, what changed and any other recorded values.
 */
export function AuditEntryItem({ entry, compact = false }: { entry: AuditEntry; compact?: boolean }) {
  const { t } = useTranslation('audit');
  const fmt = useFormat();
  const [open, setOpen] = useState(false);
  const category = categoryOf(entry.area);
  const Icon = category ? ICONS[category] : ShieldCheck;
  const dev = device(entry.user_agent);
  const diff = changes(entry.before, entry.after);
  const meta = metadataEntries(entry.metadata);

  return (
    <li className="border-b last:border-b-0" data-testid="audit-entry" data-action={entry.action}>
      <button
        type="button"
        className="hover:bg-accent/50 focus-visible:ring-ring/50 flex w-full min-w-0 items-start gap-3 px-4 py-3 text-start outline-none focus-visible:ring-[3px]"
        aria-expanded={open}
        onClick={() => {
          setOpen((o) => !o);
        }}
      >
        <Icon aria-hidden className="text-muted-foreground mt-0.5 size-4 shrink-0" />
        <span className="min-w-0 flex-1 space-y-1">
          <span className="block text-sm break-words" data-testid="audit-summary">
            {entry.summary}
          </span>
          <span className="text-muted-foreground flex flex-wrap gap-x-2 gap-y-0.5 text-xs">
            <time dateTime={entry.created_at} title={fmt.dateTime(entry.created_at, DATE_TIME)}>
              {fmt.relative(entry.created_at)}
            </time>
            {entry.ip && <span className="font-mono">{entry.ip}</span>}
            {compact && dev && <span>{dev}</span>}
            {!compact && <span className="font-mono">{entry.action}</span>}
          </span>
        </span>
        {open ? <ChevronDown aria-hidden className="mt-0.5 size-4 shrink-0" /> : <ChevronRight aria-hidden className="mt-0.5 size-4 shrink-0 rtl:rotate-180" />}
      </button>
      {open && (
        <div className="bg-muted/40 space-y-3 px-4 py-3 ps-11 text-sm" data-testid="audit-details">
          <dl className="grid grid-cols-[minmax(0,max-content)_minmax(0,1fr)] gap-x-4 gap-y-1">
            <dt className="text-muted-foreground">{t('entry.action')}</dt>
            <dd className="font-mono break-all">{entry.action}</dd>
            <dt className="text-muted-foreground">{t('entry.actor')}</dt>
            <dd className="break-words">
              {t(`entry.actorTypes.${entry.actor.type}`)}
              {entry.actor.name && ` · ${entry.actor.name}`}
              {entry.actor.email && <span className="text-muted-foreground"> · {entry.actor.email}</span>}
            </dd>
            <dt className="text-muted-foreground">{t('entry.resource')}</dt>
            <dd className="font-mono break-all">
              {entry.resource_type}
              {entry.resource_id && ` ${entry.resource_id}`}
            </dd>
            {entry.project && (
              <>
                <dt className="text-muted-foreground">{t('entry.project')}</dt>
                <dd className="break-words">{entry.project.name ?? entry.project.id}</dd>
              </>
            )}
            {entry.ip && (
              <>
                <dt className="text-muted-foreground">{t('entry.ip')}</dt>
                <dd className="font-mono">{entry.ip}</dd>
              </>
            )}
            {entry.user_agent && (
              <>
                <dt className="text-muted-foreground">{t('entry.device')}</dt>
                <dd className="break-words" title={entry.user_agent}>
                  {dev}
                </dd>
              </>
            )}
            <dt className="text-muted-foreground">{t('entry.time')}</dt>
            <dd>{fmt.dateTime(entry.created_at, DATE_TIME)}</dd>
          </dl>
          {diff.length > 0 && (
            <div className="space-y-1">
              <p className="font-medium">{t('entry.changes')}</p>
              <div className="overflow-x-auto rounded-md border">
                <table className="w-full text-xs">
                  <thead className="bg-muted/60">
                    <tr>
                      <th className="px-2 py-1 text-start font-medium">{t('entry.field')}</th>
                      <th className="px-2 py-1 text-start font-medium">{t('entry.before')}</th>
                      <th className="px-2 py-1 text-start font-medium">{t('entry.after')}</th>
                    </tr>
                  </thead>
                  <tbody className="font-mono">
                    {diff.map((c) => (
                      <tr key={c.key} className="border-t align-top">
                        <td className="px-2 py-1 break-all">{c.key}</td>
                        <td className={cn('px-2 py-1 break-all', c.before !== undefined && 'text-destructive')}>{c.before ?? '—'}</td>
                        <td className={cn('px-2 py-1 break-all', c.after !== undefined && 'text-success')}>{c.after ?? '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
          {meta.length > 0 && (
            <div className="space-y-1">
              <p className="font-medium">{t('entry.details')}</p>
              <dl className="grid grid-cols-[minmax(0,max-content)_minmax(0,1fr)] gap-x-4 gap-y-1 font-mono text-xs">
                {meta.map(([k, v]) => (
                  <div key={k} className="contents">
                    <dt className="text-muted-foreground break-all">{k}</dt>
                    <dd className="break-all">{v}</dd>
                  </div>
                ))}
              </dl>
            </div>
          )}
          {diff.length === 0 && meta.length === 0 && <p className="text-muted-foreground text-xs">{t('entry.noChanges')}</p>}
        </div>
      )}
    </li>
  );
}
