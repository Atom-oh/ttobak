# Public user guide

The user explicitly requested this Korean public-facing guide on 2026-09-17.
The language exception is limited to the HTML guide; repository engineering
references remain English. Examples are fictional and contain no meeting data.

`index.html`, `style.css`, and `guide.js` form a standalone, dependency-free site.
The guide covers all primary navigation areas, recording, review, the simulator,
sharing, follow-up, settings, and recovery. Statements describe source behavior,
not a claim that all optional features passed production acceptance.

Preview from the repository root:

```bash
python3 -m http.server 8080 --directory docs/user-guide
```

Open the local server in a browser. Navigation works without JavaScript;
JavaScript adds section search and printing. Use the browser print dialog to
save the complete guide as PDF. Search filters navigation, never printable content.

The Pages workflow validates local assets and anchors, then copies only the three
public site files into its artifact. It never publishes the repository's internal
reference archive. Enable GitHub Pages with the Actions build source. Main-branch
pushes deploy automatically after merge, including unrelated changes so that
superseded builds always have a replacement run;
pull requests validate without deployment permissions. Manual deployment is
restricted to main. No application API, credentials, or AWS deployment is used.
The complete workflow uses GitHub's FIFO `queue: max` concurrency mode, so an
older run cannot replace the newest pending run. A main-SHA check in the build
job skips superseded builds and old manual reruns before the deployment job
enters the Pages environment. Official actions are pinned to immutable commits.
Every deployment attempt also rechecks main, including failed-job-only reruns
that reuse old build outputs. A stale deployment retry fails before publication.
If main advances after that check, the active deployment may briefly publish its
reviewed revision; the newer queued run follows. This is sequential publication,
not an atomic transaction with future pushes.
GitHub allows up to 100 waiting runs; after a queue overflow or failed build,
dispatch a fresh workflow on main to publish the current guide.
See GitHub's [workflow concurrency documentation](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency).
Verified on 2026-09-17: the official documentation includes `queue: max`, its
100-run capacity, and its incompatibility with `cancel-in-progress: true`.
[PR validation run 35199025849](https://github.com/Atom-oh/ttobak/actions/runs/35199025849)
accepted this concurrency mapping and passed build and publication-boundary tests
at revision `3209055132b01fa1fba57d4ed84f5988a930c0dc`. Offline recollections that
concurrency only supports `group` and `cancel-in-progress` predate this feature.

Verify content against these sources when updating:

- `frontend/src/components/layout/Sidebar.tsx` and `frontend/src/app/`
- `frontend/src/components/meeting/SimCard.tsx`, `backend/internal/service/sim.go`,
  and `backend/python/sim/pricing.py`
- `frontend/src/components/record/`, `frontend/src/components/AudioUploader.tsx`
- `frontend/src/components/ShareButton.tsx`, `DocDetailClient.tsx`,
  `AccountsClient.tsx`, `ProjectDetailClient.tsx`, and `ResearchChat.tsx`

Do not imply universal pricing coverage, arbitrary manual requirement entry,
editor access to owner-only simulation, automatic sharing through classification,
or that upload/preview/indexing alone proves AI source acceptance.
