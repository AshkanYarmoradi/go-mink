## ADDED Requirements

### Requirement: Accurate, accessible product introduction

The website SHALL render its core product explanation, installation command, capability
descriptions, and documentation links in the initial HTML. Its statements MUST match
the implemented Go APIs and distinguish the non-durable memory adapter from PostgreSQL.
It MUST NOT present unqualified performance, compliance, or endorsement claims.

#### Scenario: Initial static response
- **WHEN** a visitor loads the homepage without client-side JavaScript
- **THEN** the product explanation and links remain readable and actionable
- **AND** no essential claim depends on a counter that initially displays zero.

#### Scenario: Capability description
- **WHEN** a reader evaluates projection support
- **THEN** the copy describes explicit projection processing rather than claiming every
  event append automatically updates read models.

### Requirement: Deterministic event replay

The in-browser demonstration SHALL derive visible state from an ordered event fixture
and a selected prefix. Replaying or revisiting a position MUST yield the same state.
Changing the selected position MUST NOT mutate the event history. The interface SHALL
identify the demonstration as a local example rather than a real connected system.

#### Scenario: Historical state selection
- **WHEN** a visitor selects an earlier event position after viewing the final position
- **THEN** the displayed state matches the prefix ending at that position
- **AND** the complete event history remains available for further selection.

#### Scenario: Reset or repeated interaction
- **WHEN** a visitor resets or changes selection during an animated replay
- **THEN** obsolete scheduled updates do not overwrite the newly selected state
- **AND** reset returns to the documented initial state.

### Requirement: Checked Go code examples

The code presentation SHALL use valid package signatures and handle errors explicitly.
Fragments MUST identify needed context or link a complete example. Complete examples
SHALL be verified against the actual Go API before publication.

#### Scenario: Reader runs an example
- **WHEN** a reader follows the complete memory-backed example
- **THEN** it compiles and demonstrates the advertised append/read behavior without a
  PostgreSQL server or ignored setup errors.

#### Scenario: Clipboard access is unavailable
- **WHEN** a visitor requests a code copy and the browser denies clipboard access
- **THEN** the interface does not report successful copying
- **AND** the code remains readable and selectable.

### Requirement: Responsive, reduced-motion interaction

The landing page SHALL support narrow mobile and wide desktop layouts without page-level
horizontal overflow. Interactive controls MUST expose accessible names, visible focus,
and keyboard operation. Reduced-motion preferences MUST suppress decorative motion while
preserving access to all demonstration states and content.

#### Scenario: Narrow viewport
- **WHEN** the homepage is viewed at a 375 CSS-pixel width
- **THEN** headings, controls, and primary navigation remain usable without horizontal
  page scrolling, while long code may scroll within its own container.

#### Scenario: Reduced motion
- **WHEN** the visitor prefers reduced motion
- **THEN** essential state changes remain available without decorative travel or looping
  animation and without initially hidden content.
