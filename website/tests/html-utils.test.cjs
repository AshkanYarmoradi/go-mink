const { test } = require("node:test");
const assert = require("node:assert/strict");
const { decode } = require("../scripts/html-utils.cjs");
test("HTML attribute decoding is single-pass and preserves encoded entity text", () => {
  for (const [input, expected] of [
    ["a=1&amp;b=2", "a=1&b=2"],
    ["&quot;hello&#39;", "\"hello'"],
    ["&amp;lt;script&amp;gt;", "&lt;script&gt;"],
    ["&amp;amp;", "&amp;"],
    ["&lt;go&gt;", "<go>"],
    ["&unknown;", "&unknown;"],
  ])
    assert.equal(decode(input), expected);
});
