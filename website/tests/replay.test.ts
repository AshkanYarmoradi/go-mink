import assert from "node:assert/strict";
import test from "node:test";
const { demoEvents, projectOrder } = await import(
  new URL("../src/components/Landing/replay.ts", import.meta.url).href
);

test("each event prefix produces the expected order state", () => {
  const expected = [
    { version: 0, customer: "—", items: 0, total: "€0.00", status: "Empty" },
    {
      version: 1,
      customer: "Ada",
      items: 0,
      total: "€0.00",
      status: "Created",
    },
    {
      version: 2,
      customer: "Ada",
      items: 2,
      total: "€58.00",
      status: "Awaiting payment",
    },
    { version: 3, customer: "Ada", items: 2, total: "€58.00", status: "Paid" },
    {
      version: 4,
      customer: "Ada",
      items: 2,
      total: "€58.00",
      status: "Shipped",
    },
  ];
  for (const [position, state] of expected.entries()) {
    assert.deepEqual(projectOrder(position), state, `event prefix ${position}`);
  }
  assert.equal(demoEvents.length, expected.length - 1);
});

test("rewinding and replaying cannot accumulate duplicate state", () => {
  const originalEvents = structuredClone(demoEvents);
  const complete = projectOrder(demoEvents.length);
  for (const position of [0, 1, 2, 3, 4, 2, 0, 4, 1, 4]) {
    projectOrder(position);
  }
  assert.deepEqual(projectOrder(demoEvents.length), complete);
  assert.equal(complete.items, 2);
  assert.deepEqual(demoEvents, originalEvents);
});

test("prefix bounds clamp safely to whole recorded event counts", () => {
  const cases: Array<[number, number]> = [
    [-3, 0],
    [-Infinity, 0],
    [NaN, 0],
    [0.99, 0],
    [2.9, 2],
    [12, 4],
    [Infinity, 4],
  ];
  for (const [input, expectedPosition] of cases) {
    assert.deepEqual(
      projectOrder(input),
      projectOrder(expectedPosition),
      String(input),
    );
  }
});
