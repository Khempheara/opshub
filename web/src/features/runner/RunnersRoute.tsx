import { RequirePermission } from '@/app/guards';
import { OrgAction } from '@/app/permissions';
import { RunnersPage } from './RunnersPage';

/** /o/:orgSlug/runners — Developers and up (runner.view). */
export function RunnersRoute() {
  return (
    <RequirePermission action={OrgAction.runnerView}>
      <RunnersPage />
    </RequirePermission>
  );
}
