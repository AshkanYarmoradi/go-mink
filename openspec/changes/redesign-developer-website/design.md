## Context

The website is a Docusaurus static build hosted by GitHub Pages at `go-mink.dev`. The
domain also resolves the Go module through HTML `go-import` and `go-source` metadata.
Documentation is authored in Markdown/MDX with existing sidebar IDs, tutorial routes,
blog-series routes, and legacy redirects. These are public interfaces to preserve.

The initial repository comparison found identical `origin/main` and `origin/develop`
trees. Main contains additional merge commits, but no unrelated file delta. Recheck this
before publication because other contributors may push while the redesign is in progress.

## Goals

- Explain what event sourcing preserves and what go-mink supplies before presenting
  advanced features.
- Give readers a useful interactive model, accurate examples, and direct next steps.
- Make reference material readable and searchable on desktop and mobile.
- Deliver a fast static site with accessible controls and deliberate, restrained motion.

## Decisions

### Keep the existing static architecture

Retain Docusaurus, GitHub Pages, the custom domain, and all established documentation
routes. This uses the site's existing Markdown, navigation, syntax highlighting,
canonical metadata, sitemap, and link validation instead of introducing a new framework
or content migration. New work lives in website components, styling, a small local search
plugin, and build verification.

### Use a coherent developer interface

Use deep ink surfaces, cyan accents, readable neutral text, consistent borders, and
purposeful spacing. The visual hierarchy should make the promise, installation command,
interactive model, capability groups, examples, and learning paths easy to distinguish.
Documentation shares the same tokens while prioritizing line length, headings, code,
sidebars, tables of contents, and mobile navigation.

### Model replay as a deterministic local demonstration

The demonstration uses an explicit finite fixture of domain events and derives visible
state from the selected prefix of that fixture. Selecting an earlier point reconstructs
that historical state; it does not mutate or remove stored events. Its controls are
keyboard accessible and expose meaningful names and state. Replaying, stepping, or
resetting must be repeatable and cancel stale animation work. No network or database is
implied: the UI clearly describes the example as an in-browser demonstration.

Test the derivation at the initial state, intermediate events, final event, and boundary
inputs. Preserve static explanatory content when scripts are unavailable. Reduced-motion
preferences remove decorative motion without removing state transitions or controls.

### Keep code claims close to evidence

Homepage snippets use real constructor signatures, initialization, context, event types,
and error handling. Provide enough surrounding context or a linked complete example to
make fragments usable. Verify complete examples with Go tooling; do not hide ignored
errors to shorten a snippet. Distinguish the non-durable memory adapter from PostgreSQL,
and make projection invocation explicit rather than suggesting append automatically
updates every read model. Discuss capability and tradeoffs instead of repeating
unqualified benchmark or compliance claims.

### Build a local full-text index

Generate search data from the built documentation or the authoritative content pipeline
so indexed targets use the same routes users can open. Ship the index as a same-origin
static artifact and load it on demand. Normalize and rank queries locally. Render title,
context, and destination in keyboard-accessible results; account for empty queries, no
matches, loading, and fetch failure. The visible trigger and Cmd/Ctrl+K open the search
dialog, Escape closes it, and closing restores focus to the initiating control. Keep
focus within the dialog while it is open.

Table-driven search tests cover case/whitespace normalization, multi-word queries,
ranking, empty/no-result queries, and preservation of actual documentation URLs. Build
verification checks generated index presence and valid targets. The search implementation
must not rely on a hosted account, secret, or external search endpoint.

### Treat the domain as an API boundary

Keep `url`, `baseUrl`, `static/CNAME`, `go-import`, and `go-source` unchanged. Verify these
in generated HTML, along with one canonical URL, a usable social image, the sitemap,
and established redirect destinations. The social image should be a static browser-
compatible asset with readable title text at preview size.

### Separate website publication from module releases

The documentation workflow builds PRs to `develop` and `main`, but deploys only pushes
to `main`. Its current `workflow_dispatch` entry builds without deploying. The Pages
environment also permits only `main`. Keep this route; do not relax the environment or
ship arbitrary feature branches to production.

The RC and stable release workflows previously ignored root Markdown and `docs/**`, but
not `website/**` or `openspec/**`. Add those paths and the two release workflow files to
both ignore lists. Ignoring the release workflows themselves prevents this configuration
change from creating a module tag. Any push containing a genuine library change still
matches the workflows and creates the usual release. Documentation deployment remains
independent and is not disabled by these exclusions.

Publication uses feature PR to `develop`, then a `develop` to `main` PR. Main's source-
branch check requires this route. Both branches have review and code-quality rules; the
publishing agent must use the repository's permitted process and report any pending
review rather than describing an unmerged PR as live.

## Risks and mitigations

- **Motion hides static content:** render complete headings/content initially and use
  animation as enhancement; test reduced motion and generated HTML.
- **Search routes drift:** derive index targets from actual content routes and validate
  them against build output; retain a direct documentation link on failure.
- **Go imports break:** assert the exact vanity metadata and custom domain in the build
  and recheck the live `?go-get=1` response after deployment.
- **Snippet/API drift:** compile or run complete example sources and link them from the
  code presentation.
- **Publication includes unrelated changes:** inspect the final `develop` to `main` tree
  diff and stop promotion if its scope exceeds this task.
- **Visual regressions:** inspect production-build screenshots on a wide desktop and
  narrow mobile viewport, plus a representative reference page, search, and replay state.

## Validation and rollout

Run the website's typecheck, behavioral tests, and production build with broken links
treated as errors. Validate generated metadata, redirects, search targets, and social
assets. Exercise replay, code tabs/copy actions, search keyboard behavior, mobile
navigation, and reduced motion in the browser. Verify Go example output independently.
Then commit and open the feature PR targeting `develop`, honor CI/review status, and
promote to `main` using the existing deployment workflow when ready. Verify HTTPS,
homepage content, a documentation route, search data, and Go vanity metadata on the live
domain. Rollback is a normal revert of the website change followed by the same workflow;
no data or database migration is involved.
