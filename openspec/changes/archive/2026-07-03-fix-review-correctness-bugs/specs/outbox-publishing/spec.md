## ADDED Requirements

### Requirement: Webhook delivery success is 2xx only
The webhook outbox publisher SHALL treat a response as delivered only when its status
code is in the `2xx` range (`>= 200 && < 300`). Any `3xx`, `4xx`, or `5xx` response
SHALL return an error so the outbox retries or dead-letters the message, so an
unfollowed redirect or other non-success status can never be recorded as delivered.

#### Scenario: A 3xx response is not treated as delivered
- **WHEN** a webhook endpoint returns `304` (or another `3xx` the HTTP client does not auto-follow for a POST)
- **THEN** `sendMessage` returns an error and the outbox does not mark the message completed

#### Scenario: 2xx variants remain successful
- **WHEN** the endpoint returns `200`, `201`, `202`, or `204`
- **THEN** the message is recorded as delivered
