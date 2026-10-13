# Meeting PDF font

The build pins the official `pretendard` npm package to **1.3.9**. Go embeds its
unmodified `dist/public/static/alternative/Pretendard-Regular.ttf` and
`dist/LICENSE.txt` directly from the restored dependency. The lockfile pins
the package's integrity; `verify.mjs` and the renderer independently check
the exact font and license SHA-256 digests. No font is fetched at runtime.

Before compiling or testing Go packages that import `internal/service`, restore
and verify the dependency:

```bash
npm --prefix backend/internal/service/pdfassets ci --ignore-scripts --no-audit --no-fund
npm --prefix backend/internal/service/pdfassets run verify
npm --prefix backend/internal/service/pdfassets test
```

Run these commands from the repository root. An unprepared checkout fails Go's
embed check; a missing or changed asset fails verification. CI, local build/setup,
note evaluation and the document-conversion image restore the dependency before
compilation. `scripts/build.sh backend` and the Go deployment workflow also place
the upstream copyright and SIL Open Font License 1.1 in the API artifact as
`Pretendard-OFL.txt`. The original reserved font name remains unchanged.

The renderer embeds only used glyphs and emits a Unicode text mapping. Unsupported
characters fail explicitly; a successful PDF must not silently drop text.
This license applies to the font, not to the user's generated document.
