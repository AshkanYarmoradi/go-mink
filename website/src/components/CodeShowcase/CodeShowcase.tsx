import React, { useState } from "react";
import { motion, AnimatePresence, useReducedMotion } from "framer-motion";
import CodeBlock from "@theme/CodeBlock";
import SectionReveal from "../shared/SectionReveal";
import GradientText from "../shared/GradientText";

const tabs = [
  {
    id: "quickstart",
    label: "Quick Start",
    code: `package main

import (
    "context"
    "go-mink.dev"
    "go-mink.dev/adapters/postgres"
)

func main() {
    ctx := context.Background()

    // Connect to your database
    store, _ := mink.NewEventStore(
        postgres.NewAdapter("postgres://localhost/orders"),
    )

    // Create and save an aggregate
    order := NewOrder("order-123")
    order.Create("customer-456")
    order.AddItem("SKU-001", 2, 29.99)

    store.SaveAggregate(ctx, order)
}`,
  },
  {
    id: "aggregates",
    label: "Aggregates",
    code: `type Order struct {
    mink.AggregateBase
    CustomerID string
    Items      []OrderItem
    Status     string
    Total      float64
}

func (o *Order) Create(customerID string) error {
    return o.Apply(&OrderCreated{
        OrderID:    o.AggregateID(),
        CustomerID: customerID,
        CreatedAt:  time.Now(),
    })
}

func (o *Order) ApplyEvent(event interface{}) error {
    switch e := event.(type) {
    case *OrderCreated:
        o.CustomerID = e.CustomerID
        o.Status = "created"
    case *ItemAdded:
        o.Items = append(o.Items, e.Item)
        o.Total += e.Item.Price * float64(e.Item.Qty)
    }
    return nil
}`,
  },
  {
    id: "projections",
    label: "Projections",
    code: `type OrderSummaryProjection struct {
    summaries map[string]*OrderSummary
}

func (p *OrderSummaryProjection) HandleEvent(
    ctx context.Context,
    event mink.StoredEvent,
) error {
    switch e := event.Data.(type) {
    case *OrderCreated:
        p.summaries[e.OrderID] = &OrderSummary{
            OrderID:    e.OrderID,
            CustomerID: e.CustomerID,
            Status:     "created",
        }
    case *ItemAdded:
        summary := p.summaries[e.OrderID]
        summary.ItemCount++
        summary.Total += e.Price * float64(e.Quantity)
    }
    return nil
}`,
  },
  {
    id: "testing",
    label: "Testing",
    code: `func TestOrder_Create(t *testing.T) {
    order := NewOrder("order-1")

    bdd.Given(t, order).
        When(func() error {
            return order.Create("customer-1")
        }).
        Then(&OrderCreated{
            OrderID:    "order-1",
            CustomerID: "customer-1",
        })
}

func TestOrder_AddItem_RequiresCreated(t *testing.T) {
    order := NewOrder("order-1")

    bdd.Given(t, order).
        When(func() error {
            return order.AddItem("SKU-1", 1, 9.99)
        }).
        ThenError(ErrOrderNotCreated)
}`,
  },
  {
    id: "encryption",
    label: "Encryption",
    code: `// Field-level encryption with envelope encryption
provider := local.NewProvider(masterKey)

store, _ := mink.NewEventStore(
    adapter,
    mink.WithEncryption(provider, mink.FieldEncryptionConfig{
        "UserRegistered": {"Email", "Phone", "Address"},
        "OrderPlaced":    {"CreditCard", "BillingAddress"},
    }),
)

// GDPR crypto-shredding: revoke the key
// All encrypted data becomes permanently unrecoverable
provider.RevokeKey(ctx, tenantKeyID)

// Data export for GDPR right to access
exporter := mink.NewDataExporter(store)
data, _ := exporter.Export(ctx, "tenant-123")`,
  },
];

export default function CodeShowcase() {
  const [activeTab, setActiveTab] = useState("quickstart");
  const shouldReduceMotion = useReducedMotion();

  const activeCode = tabs.find((t) => t.id === activeTab)?.code ?? "";

  return (
    <section className="relative py-24 px-4 sm:px-6 lg:px-8">
      <div className="max-w-4xl mx-auto">
        <SectionReveal>
          <div className="text-center mb-12">
            <h2 className="text-3xl sm:text-4xl md:text-5xl font-extrabold text-white mb-4">
              <GradientText>Clean, idiomatic</GradientText> Go
            </h2>
            <p className="text-lg text-[#94a3b8] max-w-xl !mx-auto">
              No magic. No ceremony. Just the patterns Go developers expect.
            </p>
          </div>
        </SectionReveal>

        <SectionReveal delay={0.1}>
          <div className="relative">
            <div className="absolute -inset-2 bg-gradient-to-r from-[#00ADD8]/10 via-transparent to-[#7c3aed]/10 rounded-3xl blur-2xl" />
            <div className="relative rounded-2xl border border-white/[0.08] bg-[#0d0d14] overflow-hidden shadow-2xl">
              {/* Tab bar */}
              <div className="flex items-center border-b border-white/[0.06] bg-[#12121a]/80 overflow-x-auto">
                {tabs.map((tab) => (
                  <button
                    key={tab.id}
                    onClick={() => setActiveTab(tab.id)}
                    className={`relative px-5 py-3 text-sm font-medium transition-colors duration-200 whitespace-nowrap border-none cursor-pointer ${
                      activeTab === tab.id
                        ? "text-[#00ADD8]"
                        : "text-[#64748b] hover:text-[#94a3b8]"
                    }`}
                    style={{
                      background:
                        activeTab === tab.id
                          ? "rgba(0, 173, 216, 0.06)"
                          : "transparent",
                    }}
                  >
                    {tab.label}
                    {activeTab === tab.id && (
                      <motion.div
                        layoutId={shouldReduceMotion ? undefined : "activeTab"}
                        className="absolute bottom-0 left-0 right-0 h-0.5 bg-[#00ADD8]"
                        transition={{ duration: 0.2 }}
                      />
                    )}
                  </button>
                ))}
              </div>

              {/* Code content */}
              <div className="p-6 overflow-x-auto">
                <AnimatePresence mode="wait">
                  <motion.div
                    key={activeTab}
                    initial={shouldReduceMotion ? {} : { opacity: 0, y: 8 }}
                    animate={{ opacity: 1, y: 0 }}
                    exit={shouldReduceMotion ? {} : { opacity: 0, y: -8 }}
                    transition={{ duration: 0.15 }}
                  >
                    <CodeBlock language="go" className="text-sm font-mono leading-relaxed m-0 bg-transparent border-none p-0 shadow-none">
                      {activeCode}
                    </CodeBlock>
                  </motion.div>
                </AnimatePresence>
              </div>
            </div>
          </div>
        </SectionReveal>
      </div>
    </section>
  );
}
