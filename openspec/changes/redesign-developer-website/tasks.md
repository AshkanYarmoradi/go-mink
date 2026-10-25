## 1. Required — Content and interface foundations

- [x] 1.1 Audit current homepage statements and examples against the Go implementation.
- [x] 1.2 Define shared visual tokens, responsive layout, and accessible focus/motion rules.
- [x] 1.3 Replace the homepage narrative and capability sections with accurate content and
  explicit links to reference material; keep server-rendered content visible.

## 2. Required — Developer landing (`developer-landing`)

- [x] 2.1 Implement the deterministic event replay model and accessible controls, including
  reset, historical state selection, and interrupted/repeated interaction handling.
- [x] 2.2 Add table-driven tests for initial/intermediate/final replay states and boundaries.
- [x] 2.3 Add accurate code examples with explicit context and error handling; document
  the public Go APIs they exercise and compile/run the complete examples.
- [x] 2.4 Verify code selection/copy feedback and that blocked clipboard access leaves the
  code readable with honest fallback feedback.
- [x] 2.5 Inspect desktop/mobile layout, keyboard navigation, and reduced-motion behavior.

## 3. Required — Documentation discovery (`documentation-discovery`)

- [x] 3.1 Generate a same-origin full-text documentation index with valid route targets.
- [x] 3.2 Implement visible search plus Cmd/Ctrl+K, focus containment/restoration, Escape,
  loading, no-match, and fetch-failure states.
- [x] 3.3 Add table-driven normalization/query/ranking tests and index-target validation.
- [x] 3.4 Apply coherent documentation typography, sidebar, code, and mobile navigation
  styling; document how the search build/plugin interfaces are maintained.
- [x] 3.5 Verify a reference page, a tutorial page, search result navigation, and mobile menu.

## 4. Required — Delivery contract (`website-delivery-contract`)

- [x] 4.1 Preserve canonical URLs, legacy redirects, sitemap, custom domain, and exact
  Go vanity-import metadata; add build-output checks for these interfaces.
- [x] 4.2 Produce and verify static social sharing artwork and a coherent site identity.
- [x] 4.3 Exclude website/specification/release-workflow-only changes from RC/stable tags
  while preserving release triggers for pushes containing Go library changes.
- [x] 4.4 Run typecheck, behavioral tests, and production build; verify production output.
- [ ] 4.5 Create feature PR targeting `develop`, check CI/review results, and recheck the
  promotion diff before opening the `develop` to `main` publication PR.
- [ ] 4.6 After deployment, verify HTTPS, new homepage, documentation/search assets, and
  `?go-get=1` metadata at `go-mink.dev`; record publication or pending-review status.

## 5. Good-to-have — Finishing details

- [x] 5.1 Add restrained transition/micro-interaction polish without obscuring content.
- [x] 5.2 Improve the introductory documentation's first-example guidance without
  changing its route or suggesting the memory adapter is durable.
