import { RequirePermission } from '@/app/guards';
import { OrgAction } from '@/app/permissions';
import { IngestTokensPage } from './IngestTokensPage';

/** /o/:orgSlug/logs/tokens — Admins and Owners (logs.manage). */
export function IngestTokensRoute() {
  return (
    <RequirePermission action={OrgAction.logsManage}>
      <IngestTokensPage />
    </RequirePermission>
  );
}
