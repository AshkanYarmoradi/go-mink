# Website development

The go-mink.dev website is a static Docusaurus site deployed to GitHub Pages.
Node 24+ is required. Go uses the version declared in the repository's go.mod.

## Local workflow

From `website/`:

```sh
npm ci
npm run typecheck
npm test
npm run check:examples
npm run build
npm run verify:build
npm run serve -- --port 4324
```

From the repository root, `go test ./website/examples/quickstart` verifies the
complete examples displayed on the homepage. The snippets are function excerpts;
the accompanying source includes event types, imports, setup, and error handling.
PostgreSQL setup is compiled, but running it requires `DATABASE_URL` and the `-postgres` flag.

## Design and content

- `src/components/Landing/`: event explorer, install command, and code tabs.
- `src/components/Landing/replay.ts`: deterministic educational replay model; it
  never calls a database. Playback starts only after a visitor presses Play.
- `src/data/examples.ts`: snippets checked against the runnable Go functions.
- `src/css/custom.css`: site tokens, landing layout, and responsive/reduced-motion rules.
- `src/css/docs.css`: documentation reading surface and search dialog.
- `plugins/local-search/`: build-generated, same-origin search. Its README explains
  indexing and ranking. No hosted search account or tracking is required.
- `static/img/og-image.svg`: editable source for the 1200 × 630 PNG social preview.

Keep content visible without JavaScript. Use real links for learning paths, native
controls for interactions, and finite decorative animations. The mobile event
explorer stacks its read model below the stream rather than shrinking its text.

## Delivery contracts

Do not remove `go-import` or `go-source` metadata: the domain is also the Go module
import path. Preserve CNAME, canonical URLs, the sitemap, and legacy redirects.
`verify:build` checks these contracts, every indexed documentation route, and local
links/assets in the generated HTML. It does not test external sites or hash anchors.

Changes follow feature branch → develop PR → main PR. The Pages environment only
publishes main. Compare the main/develop file trees before promotion so unrelated
library work is not released with a website change. Documentation-only changes do
not create RC or stable library tags. Respect required reviews and status checks.
