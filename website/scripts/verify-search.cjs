const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { searchDocuments } = require("../plugins/local-search/search.cjs");

const build = path.resolve(__dirname, "../build");
const indexPath = path.join(build, "search-index.json");
assert.ok(
  fs.existsSync(indexPath),
  "Build the website before verifying the search index.",
);
const documents = JSON.parse(fs.readFileSync(indexPath, "utf8"));
assert.ok(
  documents.length > 0,
  "The generated index must contain documentation.",
);
const urls = new Set();
for (const document of documents) {
  for (const field of ["title", "url", "section", "description", "text"]) {
    assert.equal(
      typeof document[field],
      "string",
      `Missing ${field} on ${document.url}`,
    );
  }
  assert.ok(
    document.url.startsWith("/docs/"),
    `Expected a documentation permalink: ${document.url}`,
  );
  assert.ok(
    !urls.has(document.url),
    `Duplicate search result: ${document.url}`,
  );
  urls.add(document.url);
  const relativePath = document.url.replace(/^\//, "");
  assert.ok(
    fs.existsSync(path.join(build, relativePath, "index.html")) ||
      fs.existsSync(path.join(build, `${relativePath}.html`)),
    `Search result has no generated page: ${document.url}`,
  );
}
assert.ok(
  urls.has("/docs/tutorial/setup"),
  "Search must use the tutorial permalink, not its numbered source filename.",
);
assert.ok(
  searchDocuments(documents, "ErrConcurrencyConflict").length > 0,
  "API symbols must remain searchable.",
);
assert.ok(
  searchDocuments(documents, "projections").some(
    (doc) => doc.url === "/docs/core/read-models",
  ),
  "The read-model reference should be discoverable by its projection headings.",
);
console.log(
  `Search verified: ${documents.length} unique document routes, API symbols, and reference ranking.`,
);
