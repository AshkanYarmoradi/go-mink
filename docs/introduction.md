---
layout: default
title: Introduction
nav_order: 2
permalink: /docs/introduction
---

# Introduction
{: .no_toc }

{: .label .label-green }
v0.5.0 Development

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## The Problem

Building Event Sourcing systems in Go today requires significant boilerplate code:

```go
// Typical boilerplate you write today:
type EventStore interface {
    Append(ctx context.Context, streamID string, events []Event) error
    Load(ctx context.Context, streamID string) ([]Event, error)
}

type Event struct {
    ID        string
    Type      string
    Data      []byte
    Metadata  map[string]string
    Timestamp time.Time
    Version   int64
}

// Then you implement for each database...
// Then you build projections manually...
// Then you handle concurrency...
// Then you build read models...
// Then you handle retries...
// Then you build migrations...
```

### Current Go Ecosystem Gaps

| Gap | Impact |
|-----|--------|
| **No unified library** | Teams reinvent the wheel |
| **Fragmented solutions** | Combine 5+ libraries for full ES |
| **Manual projections** | Error-prone, time-consuming |
| **No schema management** | Manual migration scripts |
| **Limited multi-tenancy** | Custom implementation required |
| **Poor developer UX** | Steep learning curve |

## Why Event Sourcing?

```
Traditional CRUD:                     Event Sourcing:
┌─────────────────┐                  ┌─────────────────┐
│  Current State  │                  │  Event 1        │
│  (overwritten)  │                  │  Event 2        │
└─────────────────┘                  │  Event 3        │
                                     │  ...            │
                                     │  Event N        │
                                     └─────────────────┘
                                            │
                                     ┌──────▼──────┐
                                     │ Rebuild to  │
                                     │ any state   │
                                     └─────────────┘
```

### Event Sourcing Benefits

- **Complete Audit Trail**: Every change is recorded
- **Temporal Queries**: "What was the state on March 15th?"
- **Debug Production**: Replay events locally
- **Event Replay**: Fix bugs by reprocessing events
- **Decoupled Systems**: Events enable loose coupling

## go-mink's Goals

### Primary Goals

1. **Zero Boilerplate**: Write business logic, not infrastructure
2. **Pluggable Storage**: PostgreSQL today, MongoDB tomorrow
3. **Automatic Projections**: Define once, update automatically
4. **Developer Experience**: CLI tools, testing utilities, clear errors
5. **Production Ready**: Battle-tested patterns, observability built-in

### Design Principles

| Principle | Implementation |
|-----------|----------------|
| **Convention over Configuration** | Sensible defaults, override when needed |
| **Explicit over Implicit** | No magic, clear data flow |
| **Composition over Inheritance** | Interfaces and embedding |
| **Fail Fast** | Validate early, clear error messages |
| **Observable by Default** | Metrics, tracing, logging hooks |

## Target Audience

- **Startups**: Ship event-sourced systems fast
- **Enterprise**: Reliable audit trails and compliance
- **Platform Teams**: Standardized ES infrastructure
- **Solo Developers**: Learn ES without complexity

## Success Metrics

| Metric | Target |
|--------|--------|
| Lines of code to get started | < 20 |
| Time to first event stored | < 5 minutes |
| Projection definition | < 10 lines |
| Database adapter switch | Config change only |

---

Next: [Architecture →](architecture)
