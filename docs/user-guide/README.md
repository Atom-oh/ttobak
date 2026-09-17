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
changes to this directory or the workflow deploy automatically after merge;
pull requests validate without deployment permissions. Manual deployment is
restricted to main. No application API, credentials, or AWS deployment is used.
Inside the serialized deployment job, a main-SHA check skips superseded builds
and old manual reruns. Official actions are pinned to immutable commit SHAs.

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
