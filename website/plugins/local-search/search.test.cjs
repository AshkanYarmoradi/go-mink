const test = require("node:test");
const assert = require("node:assert/strict");
const {
  plainText,
  makeRecord,
  searchDocuments,
  excerpt,
} = require("./search.cjs");

test("index respects actual permalinks and preserves searchable API symbols", () => {
  const record = makeRecord(
    {
      id: "tutorial/setup",
      title: "Project setup",
      permalink: "/docs/tutorial/setup",
      description: "Start here",
    },
    "---\ntitle: Hidden metadata\n---\n# Setup\nUse [the store](/docs/store).\n```go\nstore.ProcessStoredEvent(ctx, event)\n```",
  );
  assert.equal(record.url, "/docs/tutorial/setup");
  assert.equal(record.section, "Tutorial");
  assert.ok(record.text.includes("ProcessStoredEvent"));
  assert.ok(!record.text.includes("Hidden metadata"));
  assert.ok(!record.text.includes("/docs/store"));
});

test("search uses every term, prioritizes titles, handles case and empty results", () => {
  const documents = [
    {
      title: "Overview",
      url: "/overview",
      section: "Docs",
      description: "",
      text: "PostgreSQL adapters support snapshots.",
    },
    {
      title: "PostgreSQL adapters",
      url: "/adapters",
      section: "Core",
      description: "",
      text: "Durable snapshots and optimistic concurrency.",
    },
    {
      title: "Snapshots",
      url: "/snapshots",
      section: "Core",
      description: "",
      text: "Memory only.",
    },
  ];
  assert.equal(
    searchDocuments(documents, "POSTGRESQL snapshots")[0].url,
    "/adapters",
  );
  assert.equal(searchDocuments(documents, "postgresql missing").length, 0);
  assert.deepEqual(searchDocuments(documents, "  "), []);
  assert.equal(searchDocuments(documents, "snapshots", 1).length, 1);
});

test("excerpt includes body-only matches and strips formatting without executing HTML", () => {
  const text = `${"Earlier text. ".repeat(30)}ErrConcurrencyConflict means the stream changed.`;
  assert.ok(
    excerpt(text, "ErrConcurrencyConflict").includes("ErrConcurrencyConflict"),
  );
  assert.ok(excerpt(text, "ErrConcurrencyConflict").startsWith("…"));
  assert.equal(
    plainText("<script>alert(1)</script> **Safe**"),
    "alert(1) Safe",
  );
});

test("section headings surface a reference page ahead of incidental mentions", () => {
  const documents = [
    makeRecord(
      {
        id: "adr/storage",
        title: "A storage decision",
        permalink: "/adr/storage",
      },
      "# A storage decision\nWe also support projections.",
    ),
    makeRecord(
      {
        id: "core/read-models",
        title: "Read models",
        permalink: "/core/read-models",
      },
      "# Read models\n## Async projections\nRun projections with checkpoints.",
    ),
  ];
  assert.equal(
    searchDocuments(documents, "projections")[0].url,
    "/core/read-models",
  );
});
