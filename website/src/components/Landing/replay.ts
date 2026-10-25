/** Deterministic teaching model; this demo never writes to an event store. */
export const demoEvents = [
  { type: "OrderCreated", detail: 'customer: "Ada"', color: "cyan" },
  { type: "ItemAdded", detail: 'sku: "GO-01", qty: 2', color: "blue" },
  { type: "PaymentCaptured", detail: "amount: 58.00", color: "violet" },
  { type: "OrderShipped", detail: 'tracking: "MINK-042"', color: "green" },
] as const;
export function projectOrder(position: number) {
  const version = Math.max(
    0,
    Math.min(demoEvents.length, Math.trunc(position) || 0),
  );
  return {
    version,
    customer: version > 0 ? "Ada" : "—",
    items: version >= 2 ? 2 : 0,
    total: version >= 2 ? "€58.00" : "€0.00",
    status: ["Empty", "Created", "Awaiting payment", "Paid", "Shipped"][
      version
    ],
  };
}
