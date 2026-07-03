## ADDED Requirements

### Requirement: Code generation does not overwrite existing files without --force
`mink generate` SHALL refuse to overwrite an existing target file unless a `--force`
flag is provided, so re-running generation cannot silently destroy hand-written code. In
the absence of `--force`, an existing target SHALL cause the command to fail without
writing.

#### Scenario: Re-running generate without --force preserves edits
- **WHEN** `mink generate` would write a file that already exists and `--force` is not set
- **THEN** the command returns an error and leaves the existing file unchanged

#### Scenario: --force overwrites
- **WHEN** `--force` is set and the target exists
- **THEN** the existing file is overwritten

### Requirement: migrate down treats a record-removal failure as fatal
`mink migrate down` SHALL treat a failure to remove a migration record as fatal (non-nil
error, non-zero exit), symmetric with `migrate up`'s handling of a record-write failure,
so the migrations table cannot silently diverge from the applied schema.

#### Scenario: Record removal failure aborts down with an error
- **WHEN** the down SQL succeeds but removing the migration record fails
- **THEN** `migrate down` returns an error and exits non-zero rather than reporting success
