const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const build = path.resolve(__dirname, "../build");
const origin = "https://go-mink.dev";
const goImport = "go-mink.dev git https://github.com/AshkanYarmoradi/go-mink";
const goSource =
  "go-mink.dev _ https://github.com/AshkanYarmoradi/go-mink/tree/main{/dir} https://github.com/AshkanYarmoradi/go-mink/blob/main{/dir}/{file}#L{line}";

function read(file) {
  return fs.readFileSync(path.join(build, file), "utf8");
}
function decode(value) {
  return value
    .replace(/&amp;/g, "&")
    .replace(/&quot;/g, '"')
    .replace(/&#x27;|&#39;/g, "'")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">");
}
// The generated HTML uses quoted attributes. This limited parser deliberately
// inspects static output without executing the site's scripts or needing a DOM.
function tags(html, name) {
  return [...html.matchAll(new RegExp(`<${name}\\b[^>]*>`, "gi"))].map(
    ([tag]) =>
      Object.fromEntries(
        [...tag.matchAll(/([\w:-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')/g)].map(
          ([, key, double, single]) => [
            key.toLowerCase(),
            decode(double ?? single),
          ],
        ),
      ),
  );
}
function metadata(html, key) {
  return tags(html, "meta")
    .filter((tag) => tag.name === key || tag.property === key)
    .map((tag) => tag.content);
}
function canonical(html) {
  const urls = tags(html, "link")
    .filter((tag) => tag.rel === "canonical")
    .map((tag) => tag.href);
  assert.equal(
    urls.length,
    1,
    "Every page must have exactly one canonical URL.",
  );
  return urls[0];
}
function existsAt(url) {
  const pathname = decodeURIComponent(new URL(url, origin).pathname).replace(
    /^\//,
    "",
  );
  return [
    pathname || "index.html",
    `${pathname}.html`,
    path.join(pathname, "index.html"),
  ].some((candidate) => {
    const file = path.resolve(build, candidate);
    assert.ok(
      file.startsWith(`${build}${path.sep}`),
      "A local URL must remain inside the build directory.",
    );
    return fs.existsSync(file) && fs.statSync(file).isFile();
  });
}
function htmlFiles(directory) {
  return fs.readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const file = path.join(directory, entry.name);
    return entry.isDirectory()
      ? htmlFiles(file)
      : entry.name.endsWith(".html")
        ? [file]
        : [];
  });
}

assert.equal(
  read("CNAME").trim(),
  "go-mink.dev",
  "Custom domain must be preserved.",
);
const home = read("index.html");
const notFound = read("404.html");
for (const [name, html] of [
  ["homepage", home],
  ["404 fallback", notFound],
]) {
  assert.deepEqual(
    metadata(html, "go-import"),
    [goImport],
    `${name} must support the Go vanity import.`,
  );
  assert.deepEqual(
    metadata(html, "go-source"),
    [goSource],
    `${name} must preserve Go source metadata.`,
  );
}
assert.equal(canonical(home), `${origin}/`);
assert.ok(
  metadata(home, "description")[0]?.length > 50,
  "Homepage needs a useful search description.",
);
assert.equal(
  (home.match(/<h1\b/g) || []).length,
  1,
  "Homepage should have a single primary heading.",
);
assert.deepEqual(metadata(home, "og:image"), [`${origin}/img/og-image.png`]);
assert.deepEqual(metadata(home, "twitter:image"), [
  `${origin}/img/og-image.png`,
]);
assert.ok(
  metadata(notFound, "robots").some((value) => value.includes("noindex")),
  "The 404 page must not be indexed.",
);

const image = fs.readFileSync(path.join(build, "img/og-image.png"));
assert.equal(
  image.subarray(0, 8).toString("hex"),
  "89504e470d0a1a0a",
  "Social image must be a real PNG.",
);
assert.ok(
  image.readUInt32BE(16) >= 600 && image.readUInt32BE(20) >= 300,
  "Social image must have a useful preview resolution.",
);
assert.ok(read("robots.txt").includes(`Sitemap: ${origin}/sitemap.xml`));

const sitemap = read("sitemap.xml");
const sitemapUrls = [...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map(
  ([, url]) => decode(url),
);
assert.ok(sitemapUrls.includes(`${origin}/`));
assert.ok(
  !sitemapUrls.some((url) => /\/404(?:\.html)?\/?$/.test(url)),
  "404 must be omitted from the sitemap.",
);
for (const url of sitemapUrls) {
  assert.equal(
    new URL(url).origin,
    origin,
    "Sitemap should use the custom domain.",
  );
  assert.ok(existsAt(url), `Sitemap route has no generated page: ${url}`);
}

const documents = JSON.parse(read("search-index.json"));
for (const document of documents) {
  const pagePath = document.url.replace(/^\//, "");
  const html = read(
    fs.existsSync(path.join(build, pagePath, "index.html"))
      ? path.join(pagePath, "index.html")
      : `${pagePath}.html`,
  );
  assert.equal(
    canonical(html).replace(/\/$/, ""),
    `${origin}${document.url}`.replace(/\/$/, ""),
    `Incorrect canonical for ${document.url}`,
  );
}

const redirects = [
  ["/docs/introduction", "/docs/getting-started/introduction"],
  ["/docs/architecture", "/docs/getting-started/architecture"],
  ["/blog", "/docs/blog-series/introduction"],
  ["/tutorial", "/docs/tutorial/setup"],
  ...[
    "introduction",
    "getting-started",
    "aggregates",
    "event-store",
    "cqrs",
    "middleware",
    "projections",
    "production",
  ].map((slug, index) => [
    `/blog/${String(index + 1).padStart(2, "0")}-${slug}`,
    `/docs/blog-series/${slug}`,
  ]),
  ...[
    "setup",
    "domain-modeling",
    "commands-cqrs",
    "projections",
    "testing",
    "production",
  ].map((slug, index) => [
    `/tutorial/${String(index + 1).padStart(2, "0")}-${slug}`,
    `/docs/tutorial/${slug}`,
  ]),
];
for (const [from, to] of redirects) {
  const html = read(path.join(from.slice(1), "index.html"));
  assert.equal(
    new URL(canonical(html), origin).pathname,
    to,
    `Legacy redirect changed: ${from}`,
  );
  assert.ok(
    tags(html, "meta").some(
      (tag) =>
        tag["http-equiv"] === "refresh" && tag.content.endsWith(`url=${to}`),
    ),
    `Legacy redirect is not usable without JavaScript: ${from}`,
  );
}

let checkedLinks = 0;
const missing = new Set();
for (const file of htmlFiles(build)) {
  const html = fs.readFileSync(file, "utf8");
  const base = new URL(canonical(html), origin);
  for (const [name, attribute] of [
    ["a", "href"],
    ["img", "src"],
    ["script", "src"],
    ["link", "href"],
  ]) {
    for (const tag of tags(html, name)) {
      const value = tag[attribute];
      if (!value || value.startsWith("#")) continue;
      const url = new URL(value, base);
      if (url.origin !== origin) continue;
      checkedLinks++;
      if (!existsAt(url))
        missing.add(`${path.relative(build, file)} → ${url.pathname}`);
    }
  }
}
assert.deepEqual(
  [...missing],
  [],
  "Generated pages contain missing local links or assets.",
);
console.log(
  `Site verified: vanity imports, domain, canonical URLs, social PNG, ${documents.length} docs, ${redirects.length} legacy redirects, and ${checkedLinks} local links/assets.`,
);
