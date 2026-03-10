import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebars: SidebarsConfig = {
  docs: [
    {
      type: "category",
      label: "Getting Started",
      collapsed: false,
      items: ["getting-started/introduction", "getting-started/architecture"],
    },
    {
      type: "category",
      label: "Core Concepts",
      collapsed: false,
      items: [
        "core/event-store",
        "core/read-models",
        "core/adapters",
      ],
    },
    {
      type: "category",
      label: "Advanced",
      items: [
        "advanced/advanced-patterns",
        "advanced/api-design",
        "advanced/security",
        "advanced/versioning",
      ],
    },
    {
      type: "category",
      label: "Developer Guide",
      items: [
        "guide/testing",
        "guide/cli",
        "guide/benchmarks",
      ],
    },
    "roadmap",
    {
      type: "category",
      label: "Architecture Decision Records",
      collapsed: true,
      link: { type: "doc", id: "adr/index" },
      items: [
        "adr/event-sourcing-core",
        "adr/postgresql-primary-storage",
        "adr/cqrs-pattern",
        "adr/command-bus-middleware",
        "adr/projection-types",
        "adr/optimistic-concurrency",
        "adr/json-serialization",
        "adr/testing-strategy",
        "adr/error-handling",
        "adr/multi-tenancy",
      ],
    },
  ],

  tutorial: [
    {
      type: "category",
      label: "E-Commerce Tutorial",
      collapsed: false,
      link: { type: "doc", id: "tutorial/index" },
      items: [
        "tutorial/setup",
        "tutorial/domain-modeling",
        "tutorial/commands-cqrs",
        "tutorial/projections",
        "tutorial/testing",
        "tutorial/production",
      ],
    },
  ],

  blogSeries: [
    {
      type: "category",
      label: "Event Sourcing Blog Series",
      collapsed: false,
      link: { type: "doc", id: "blog-series/index" },
      items: [
        "blog-series/introduction",
        "blog-series/getting-started",
        "blog-series/aggregates",
        "blog-series/event-store",
        "blog-series/cqrs",
        "blog-series/middleware",
        "blog-series/projections",
        "blog-series/production",
      ],
    },
  ],
};

export default sidebars;
