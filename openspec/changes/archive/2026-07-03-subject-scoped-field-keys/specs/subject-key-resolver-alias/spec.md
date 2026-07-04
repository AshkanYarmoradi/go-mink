## ADDED Requirements

### Requirement: WithSubjectKeyResolver option
`FieldEncryptionConfig` SHALL accept a `WithSubjectKeyResolver(func(subjectID string) string) EncryptionOption` (options-pattern, consistent with the existing `WithX(...)` style) that configures the same resolver as `WithTenantKeyResolver`. It exists for legibility where keys are per data-subject rather than per tenant; it MUST NOT introduce a second, separately-tracked resolver.

#### Scenario: Alias is equivalent to WithTenantKeyResolver
- **GIVEN** two stores configured identically except one uses `WithSubjectKeyResolver(fn)` and the other `WithTenantKeyResolver(fn)` with the same `fn`
- **WHEN** the same events are encrypted through each
- **THEN** both resolve to the same key ids and produce equivalent encryption outcomes

#### Scenario: Later option wins when both are set
- **WHEN** both `WithTenantKeyResolver` and `WithSubjectKeyResolver` are passed to the same config
- **THEN** the last-applied option's function is used (they set one field; no ambiguity or double resolution)
