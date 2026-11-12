# Releases and tags

A release names a reviewed `main` revision so users and operators can see what
changed. It is separate from deployment: a tag does not prove that a stack,
Lambda or frontend build was deployed. Follow the [deployment runbook](deployment.md)
and [frontend release](frontend-release.md) to ship a tagged revision.

## Tag policy

| Tag | Use | Rules |
|---|---|---|
| `vMAJOR.MINOR.PATCH` | Application release (web, API and workers in this monorepo) | Annotated, on the `main` merge commit of the release PR |
| `mac-vMAJOR.MINOR.PATCH` | Mac app binary release | Independent version; see [Mac app release](mac-app-release.md) |
| other names (for example `guide-ui-2026-11-10`) | Immutable media or evidence references | Not a release; never use a `v` prefix |

- Every PR with a user-visible, API, configuration or operational change adds its
  entry under `## [Unreleased]` in [CHANGELOG.md](../../CHANGELOG.md), so a
  release only moves entries and writes the in-app notes.
- Version numbers follow Semantic Versioning for this application:
  - **MAJOR**: an incompatible `/api/*` response or parameter change, a removed
    route, or a configuration or data change that needs manual operator steps.
  - **MINOR**: a new user-visible feature, page, endpoint or compatible option.
  - **PATCH**: fixes only, with no new behavior to learn.
- Never move, delete or re-point a pushed release tag; correct a bad release with
  a new patch release.
- `frontend/package.json` carries the application version. Other component
  manifests (`mcp-server`, `infra`, `mac-app`) keep their own versions.
- `v1.0.0` (2026-11-11) is the first application release. Earlier history has no
  release tags; an unpushed local `v0.1.0` from 2026-11-09 was never published.

## Cut a release

1. On a branch from current `main`, move the `## [Unreleased]` entries of
   `CHANGELOG.md` under `## [X.Y.Z] - YYYY-MM-DD` and leave an empty
   `## [Unreleased]` above it. The changelog stays the complete English record.
2. Set the version:

   ```bash
   (cd frontend && npm version X.Y.Z --no-git-tag-version)
   ```

3. Add the release at the top of `RELEASES` in `frontend/src/lib/releaseNotes.ts`:
   the user-facing subset in Korean UI copy. Each item names where to find the
   feature with the actual UI labels, an app route when one exists, PR numbers,
   and `mac: true` for Mac-app-only behavior. Omit CI-only and internal changes.
4. Run the frontend checks:

   ```bash
   (cd frontend && npm run lint && npm run test:unit && npm run build)
   ```

   `scripts/release-notes.test.mjs` fails unless the newest note, the
   `package.json`/`package-lock.json` version and the newest changelog heading
   match, and every linked route is a static page served by `FrontendStack`.
5. Merge the release PR through the normal review, then tag the merge commit and
   publish the GitHub release:

   ```bash
   git fetch origin
   SHA=$(git rev-parse origin/main)   # confirm this is the release PR merge commit
   git tag -a vX.Y.Z "$SHA" -m "vX.Y.Z"
   git push origin refs/tags/vX.Y.Z
   awk '/^## \[X.Y.Z\]/{f=1;next} /^## \[/{f=0} f' CHANGELOG.md > notes.md
   gh release create vX.Y.Z --verify-tag --title vX.Y.Z --notes-file notes.md
   ```

   If `origin/main` has moved past the release merge, tag that merge commit
   explicitly. Push only the specific tag, never all local tags.

## In-app notes

`/changelog` (`frontend/src/components/Changelog.tsx`) renders `RELEASES`. The
version link at the bottom of the sidebar and in the mobile menu sheet shows the
latest version and a dot until this browser opens the page
(`ttobak.releaseNotesSeen` in local storage; unavailable storage keeps the dot).
The page shows the notes the deployed frontend bundle contains, not changes
merged after it.

## Limits

The in-app notes are curated summaries. They do not replace the changelog, the
API reference or [CLAUDE.md](../../CLAUDE.md), and a listed feature can still be
hidden by role, runtime configuration or the absence of the Mac app.
