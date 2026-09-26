import { RequirePermission } from '@/app/guards';
import { OrgAction } from '@/app/permissions';
import { AuditLogPage } from './AuditLogPage';

/** /o/:orgSlug/audit-log — Owners and Admins (audit.view). */
export function AuditLogRoute() {
  return (
    <RequirePermission action={OrgAction.auditView}>
      <AuditLogPage />
    </RequirePermission>
  );
}
