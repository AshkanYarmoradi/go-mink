## ADDED Requirements

### Requirement: Preserve the domain and Go vanity import contract

The static site SHALL continue using `https://go-mink.dev` with root base URL and its
existing custom-domain file. Generated HTML MUST retain `go-import` and `go-source`
metadata resolving the `go-mink.dev` module to the existing GitHub repository.

#### Scenario: Go module discovery
- **WHEN** a Go client requests `https://go-mink.dev/?go-get=1`
- **THEN** the HTML contains `go-mink.dev git https://github.com/AshkanYarmoradi/go-mink`
  in the `go-import` meta tag and the matching existing `go-source` template.

### Requirement: Preserve indexable routes and sharing metadata

Generated pages SHALL retain valid canonical URLs, the sitemap, and existing legacy
redirects. Social metadata SHALL reference an existing static image suitable for sharing.
Broken internal links MUST fail the production build.

#### Scenario: Legacy route
- **WHEN** a visitor follows an existing legacy introduction, tutorial, or blog route
- **THEN** the generated redirect points to its established destination.

#### Scenario: Build metadata verification
- **WHEN** a production build succeeds
- **THEN** its homepage has a canonical URL on `go-mink.dev`, its social image resolves
  to a generated asset, and its sitemap contains documentation routes.

#### Scenario: Broken internal destination
- **WHEN** source content links to a nonexistent internal route
- **THEN** the production build reports an error rather than publishing a broken link.

### Requirement: Website publication does not release the Go module

Website, specification, and release-workflow-only pushes SHALL be excluded from RC and
stable module release triggers. A push containing a library source change MUST retain
the current release behavior. Website deployment SHALL continue through the existing
`main` branch GitHub Pages workflow and environment.

#### Scenario: Website-only promotion
- **WHEN** a change limited to `website/**`, `openspec/**`, existing ignored documentation,
  and the documentation/release workflows is merged through `develop` to `main`
- **THEN** the website deployment runs for website changes
- **AND** no RC or stable Go module tag is created solely for those changes.

#### Scenario: Website and library changed together
- **WHEN** a push also modifies library source outside the ignored paths
- **THEN** the existing RC or stable release workflow remains eligible to run.

#### Scenario: Unrelated development changes before publication
- **WHEN** the final `develop` to `main` diff contains work outside the approved website
  scope
- **THEN** the website change is not described as safely publishable without resolving
  that additional release scope first.
