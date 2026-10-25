---
title: Introduction
description: Learn what go-mink provides, choose a starting point, and understand the flow from commands to events and read models.
sidebar_position: 2
---

# Introduction

go-mink is an Event Sourcing and CQRS toolkit for Go. It gives you the building blocks to record changes as events, rebuild domain state, and create read models for your application.

Start with a small event stream. Add commands, projections, and workflow coordination as your application needs them.

## Choose your starting point

| If you want to… | Start here |
| --- | --- |
| Build an application step by step | [The order system tutorial](/docs/tutorial/setup) |
| Understand the moving parts | [Architecture](/docs/getting-started/architecture) |
| Append events and load a stream | [The event store](/docs/core/event-store) |
| Build query-friendly views | [Read models and projections](/docs/core/read-models) |
| Compare storage capabilities | [Adapters](/docs/core/adapters) |
| Learn the concepts from first principles | [The Event Sourcing series](/docs/blog-series/introduction) |

## Install the module

From a Go module, add go-mink:

```bash
go get go-mink.dev
```

Use the [memory adapter](/docs/core/adapters) for tests and local experiments. Use PostgreSQL when your events need durable storage. The [project setup tutorial](/docs/tutorial/setup) walks through configuration, schema initialization, and your first application.

## Think in events

A conventional state update records that an order is now shipped. An event-sourced model also records the steps that got it there:

```text
OrderCreated → ItemAdded → PaymentReceived → OrderShipped
```

The event stream is the history. An aggregate applies that history to rebuild its current state. A projection transforms events into a view designed for a specific query: an order summary, a fulfillment queue, or a report.

This is useful when the sequence of changes matters. It also introduces responsibilities: event schema evolution, projection maintenance, replay safety, and operational monitoring. For a small CRUD application with no need for historical behavior, a conventional database model may be simpler.

## Follow the data

1. **Handle a command.** Your domain logic validates an action and decides which events to record. The command bus can add validation, logging, tracing, and other middleware.
2. **Persist the events.** The event store serializes events and appends them through an adapter. Expected stream versions protect against conflicting writes.
3. **Build a view.** Your application explicitly invokes inline projections, runs async projections, or notifies live projections according to its consistency needs.
4. **Query the read model.** Read a view shaped for your application instead of rebuilding an entire stream for every screen.

:::note Projections are explicit
Appending an event does not automatically run every projection. Inline projections are invoked through `ProcessInlineProjections`; async projections poll stored events and maintain checkpoints; live projections receive notifications through `NotifyLiveProjections`.
:::

## Add capabilities as you need them

- **Domain modeling:** [aggregates and the event store](/docs/core/event-store), optimistic concurrency, and snapshots.
- **Application workflows:** [the command bus, sagas, and outbox](/docs/advanced/advanced-patterns).
- **Evolution:** [event versioning and upcasting](/docs/advanced/versioning).
- **Data protection:** [field-level encryption](/docs/advanced/security) and [data governance](/docs/security).
- **Verification:** [Given–When–Then tests, assertions, and projection utilities](/docs/guide/testing).
- **Operations:** [the CLI](/docs/guide/cli), [benchmarks](/docs/guide/benchmarks), and [production guidance](/docs/tutorial/production).

The library provides mechanisms; your application defines domain rules, consistency boundaries, access controls, and retention policies. The [architecture guide](/docs/getting-started/architecture) explains how these pieces fit together.
