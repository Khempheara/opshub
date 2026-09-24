import { createContext, useContext } from 'react';
import type { ProjectAction, ProjectDetail } from '@/lib/api/generated/model';

/** The project resolved by ProjectLayout for its tabs. */
export const ProjectContext = createContext<ProjectDetail | null>(null);

export function useCurrentProject(): ProjectDetail {
  const p = useContext(ProjectContext);
  if (!p) throw new Error('useCurrentProject must be used inside ProjectLayout');
  return p;
}

/** Whether the caller may perform a project action (UI only; the API enforces it). */
export function canProject(p: ProjectDetail, action: ProjectAction): boolean {
  return p.actions.includes(action);
}
