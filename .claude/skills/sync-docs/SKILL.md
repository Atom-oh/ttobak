# Sync Docs Skill

## Trigger
When user runs `/sync-docs` or when significant code changes are detected.

## Steps
1. Detect which source files changed: `git diff --name-only HEAD~5`
2. Map changes to documentation:
   - `backend/internal/handler/*` → `docs/API-SPEC.md`
   - `infra/lib/*` → `docs/INFRA-SPEC.md`
   - `frontend/src/components/*` → `docs/DESIGN-SPEC.md`
   - Architecture changes → `docs/architecture.md`
3. For each affected doc:
   - Read current doc content
   - Read changed source files
   - Identify outdated sections
   - Update with accurate information
4. Quality score each doc (1-5): coverage, accuracy, freshness
5. Report updated docs and their quality scores

## Quality Criteria
- **5**: Fully aligned with code, all endpoints/components documented
- **4**: Minor gaps, mostly current
- **3**: Some sections outdated but structure intact
- **2**: Significant drift from code
- **1**: Severely outdated or missing critical sections
