## ADDED Requirements

### Requirement: Same-origin documentation search

The website SHALL generate a full-text documentation index during its build and serve
it as a same-origin static asset. Search MUST use the actual documentation route targets
and MUST NOT require a hosted search account, credential, or external search request.

#### Scenario: Matching documentation
- **WHEN** a visitor searches for a term occurring in documentation content
- **THEN** the search presents matching document titles and relevant context
- **AND** opening a result navigates to a generated documentation route.

#### Scenario: Query has no matches
- **WHEN** the normalized query matches no indexed document
- **THEN** the interface gives an explicit no-results state without inventing results.

#### Scenario: Search index cannot load
- **WHEN** fetching the static index fails
- **THEN** the interface reports search unavailability and provides a usable path to
  browse the documentation rather than showing a permanent loading state.

### Requirement: Keyboard search dialog

The search interface SHALL have a visible trigger and support Cmd/Ctrl+K. While open,
the dialog MUST provide a labeled query input and accessible result links, contain
keyboard focus, support Escape to close, and restore focus to its initiating control.

#### Scenario: Keyboard discovery and return
- **WHEN** a visitor opens search using its keyboard shortcut and then presses Escape
- **THEN** the dialog closes and keyboard focus returns to the element that opened it.

#### Scenario: Mobile search
- **WHEN** a visitor opens search on a narrow viewport
- **THEN** the query, close control, and result list fit the viewport and can be operated
  without horizontal page scrolling.

### Requirement: Consistent documentation reading experience

Documentation pages SHALL use the site's visual identity while retaining readable text,
code, headings, sidebar navigation, and table-of-contents hierarchy. Existing document,
tutorial, and blog-series routes MUST remain valid.

#### Scenario: Existing inbound documentation link
- **WHEN** a reader opens an established documentation route
- **THEN** the corresponding document renders with working navigation and an unchanged
  canonical route.

#### Scenario: Narrow documentation layout
- **WHEN** a reader opens a document on a mobile viewport
- **THEN** navigation remains reachable and long code/table content is contained without
  forcing the entire page wider than the viewport.
