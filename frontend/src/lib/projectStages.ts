// Fixed SFDC-aligned project stages. Keep in sync with model.ProjectStages in
// the Go backend (backend/internal/model/project.go).
export const PROJECT_STAGES = [
  'Prospect',
  'Qualified',
  'Technical Validation',
  'Business Validation',
  'Committed',
  'Launched',
  'Closed Lost',
] as const;

export type ProjectStage = (typeof PROJECT_STAGES)[number];

export const UNSET_STAGE_LABEL = '단계 미설정';

export function isTerminalStage(stage: string | undefined | null): boolean {
  return stage === 'Launched' || stage === 'Closed Lost';
}

// An unmapped legacy value (mapped === false) shows as unset; an empty stage
// has no label at all.
export function stageLabel(stage: string | undefined | null, mapped: boolean | undefined): string {
  if (!stage) return '';
  return mapped === false ? UNSET_STAGE_LABEL : stage;
}
