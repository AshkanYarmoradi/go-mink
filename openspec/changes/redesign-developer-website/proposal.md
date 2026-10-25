## Why

The go-mink website should help a Go developer understand the library, evaluate its
tradeoffs, and reach working code quickly. The current landing page uses an outdated
constructor example, unqualified performance claims, and counters that show zero in
server-rendered HTML. Its visual treatment and documentation navigation do not make
the depth of the toolkit easy to discover.

This change replaces that experience with a cohesive developer website: a distinctive
visual identity, a useful event replay demonstration, verified Go examples, and fast
local documentation search. The existing domain, documentation routes, and Go vanity
import contract remain intact.

## What Changes

### Required

- **Developer landing experience** (`developer-landing`): redesign the homepage with
  clear positioning, capability explanations grounded in the implementation, an
  interactive deterministic event replay, checked Go examples, and clear paths into
  the documentation. Use responsive layouts and purposeful motion with reduced-motion
  support. Core content must remain visible without JavaScript.
- **Documentation discovery** (`documentation-discovery`): provide consistent document
  typography and navigation plus locally indexed full-text search. Support keyboard
  operation, a visible search trigger, empty/error states, and mobile layouts without
  requiring a third-party search account.
- **Website delivery contract** (`website-delivery-contract`): preserve canonical URLs,
  existing redirects, sitemap generation, the custom domain, and `go-import`/`go-source`
  metadata. Add suitable social sharing artwork and verify the production output.
  Website-only changes must not create Go module releases.

### Good-to-have

- Add restrained decorative transitions or micro-interactions where they explain
  state or improve feedback (`developer-landing`). They must not block reading, input,
  navigation, or assistive technology.
- Improve the introductory documentation's explanation of the first working example
  while keeping the remainder of the reference content and routes stable
  (`documentation-discovery`).

### Non-Goals

- No Go public API, storage adapter, event format, migration, or library runtime changes.
- No claim of regulatory compliance, customer endorsement, benchmark speed, or test
  coverage unless specific current evidence supports the wording and its context.
- No hosted search service, analytics tracking, login, marketing backend, or new hosting
  provider. GitHub Pages continues serving the existing Docusaurus website.
- No global rewrite of the documentation or invalidation of existing inbound links.
- No publication of unrelated development changes as part of this website update.

## Capabilities

### Added Capabilities

- `developer-landing` — accessible responsive product narrative and event replay.
- `documentation-discovery` — local search and coherent documentation navigation.
- `website-delivery-contract` — static delivery, public metadata, and release isolation.

## Impact

The change is confined to the website, its supporting specifications, and documentation
delivery/release workflow configuration. New behavioral tests exercise the demonstration,
search indexing/matching, and build-output contracts. Go examples are verified against
the real package API. This is a website capability change, so requirements describe the
browser and static-site interfaces rather than inventing new Go interfaces; the existing
append-only, zero-overhead-when-unused, and no-forced-schema-change invariants are preserved
because no library behavior is modified.

The feature branch starts from `develop` and its PR targets `develop`. Publication follows
the repository's `develop` to `main` promotion route after checking that its diff contains
only the intended website work.
