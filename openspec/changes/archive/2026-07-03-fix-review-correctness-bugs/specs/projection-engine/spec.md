## ADDED Requirements

### Requirement: Checkpoint-load failure does not silently replay from zero
An async projection worker SHALL treat a non-nil error from checkpoint retrieval at
startup as a failure to start (surfaced via the engine/worker error path) rather than
defaulting its start position to 0. A *missing* checkpoint (the adapter returns position
0 with a nil error) SHALL still start from 0.

#### Scenario: Transient checkpoint error does not reprocess history
- **WHEN** a worker with an existing checkpoint at position N starts and checkpoint retrieval returns a transient error
- **THEN** the worker does not start from 0 and does not reprocess the stream from the beginning

#### Scenario: Missing checkpoint starts from zero
- **WHEN** no checkpoint exists (retrieval returns `(0, nil)`)
- **THEN** the worker starts from position 0 as before

### Requirement: Live projections do not silently drop events
`NotifyLiveProjections` SHALL NOT silently discard an event delivered while a worker is
not `Running`. The engine SHALL either catch the worker up from its last position when
it becomes `Running`, or expose the drop (a counter and a documented, observable
no-catch-up contract) so loss is intentional and visible rather than silent.

#### Scenario: Event arriving before a worker is Running is not silently lost
- **WHEN** an event is notified to a live projection whose worker is not yet `Running`
- **THEN** the event is caught up on start or recorded as a visible drop, never silently discarded with no signal

### Requirement: Poison event identity matches the applied batch
On an `ApplyBatch` failure, the poison event reported to `OnPoisonEvent` SHALL be taken
from the filtered slice actually applied to the projection, not from an unfiltered batch
that may include events the projection does not handle.

#### Scenario: Poison event is one the projection handles
- **WHEN** a batch's last loaded event is filtered out and `ApplyBatch` fails
- **THEN** `OnPoisonEvent` receives an event from the filtered/applied set, not the filtered-out trailing event
