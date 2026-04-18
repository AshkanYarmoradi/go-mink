import React from "react";
import { motion, useReducedMotion } from "framer-motion";
import Link from "@docusaurus/Link";
import CodeBlock from "@theme/CodeBlock";
import GradientText from "../shared/GradientText";
import HeroBackground from "./HeroBackground";

const codeSnippet = `store, _ := mink.NewEventStore(
    postgres.NewAdapter("postgres://localhost/orders"),
)

order := NewOrder("order-123")
order.Create("customer-456")
order.AddItem("SKU-001", 2, 29.99)

store.SaveAggregate(ctx, order)`;

export default function Hero() {
  const shouldReduceMotion = useReducedMotion();

  const containerVariants = {
    hidden: {},
    visible: {
      transition: { staggerChildren: 0.12 },
    },
  };

  const itemVariants = {
    hidden: { opacity: 0, y: 20 },
    visible: {
      opacity: 1,
      y: 0,
      transition: { duration: 0.6, ease: [0.21, 0.47, 0.32, 0.98] },
    },
  };

  const content = (
    <>
      {/* Version badge */}
      <div className="mb-6">
        <span className="inline-flex items-center gap-2 rounded-full border border-[#00ADD8]/30 bg-[#00ADD8]/10 px-4 py-1.5 text-sm font-medium text-[#00ADD8] shadow-[0_0_20px_rgba(0,173,216,0.15)]">
          <span className="h-2 w-2 rounded-full bg-[#00ADD8] animate-pulse" />
          v1.0.0 — Production Ready
        </span>
      </div>

      {/* Heading */}
      <h1 className="text-5xl sm:text-6xl md:text-7xl lg:text-8xl font-extrabold tracking-tight leading-[0.95] mb-6">
        Event Sourcing
        <br />
        <GradientText>for Go</GradientText>
      </h1>

      {/* Subheading */}
      <p className="text-lg sm:text-xl md:text-2xl text-[#94a3b8] max-w-2xl mx-auto mb-10 leading-relaxed">
        Built for developers who demand simplicity without sacrificing power.
        Production-ready event sourcing & CQRS toolkit.
      </p>

      {/* CTAs */}
      <div className="flex flex-col sm:flex-row items-center justify-center gap-4 mb-16">
        <Link
          to="/docs/getting-started/introduction"
          className="group inline-flex items-center gap-2 rounded-xl bg-gradient-to-r from-[#00ADD8] to-[#0077b6] px-8 py-3.5 text-base font-semibold text-white shadow-lg shadow-[#00ADD8]/20 transition-all duration-200 hover:shadow-xl hover:shadow-[#00ADD8]/30 hover:-translate-y-0.5 no-underline hover:no-underline hover:text-white"
        >
          Get Started
          <span className="transition-transform duration-200 group-hover:translate-x-0.5">
            &rarr;
          </span>
        </Link>
        <a
          href="https://github.com/AshkanYarmoradi/go-mink"
          className="inline-flex items-center gap-2 rounded-xl border border-white/10 bg-white/[0.03] px-8 py-3.5 text-base font-semibold text-[#e2e8f0] transition-all duration-200 hover:bg-white/[0.06] hover:border-white/20 no-underline hover:no-underline hover:text-white"
        >
          <svg className="w-5 h-5" fill="currentColor" viewBox="0 0 24 24">
            <path d="M12 0c-6.626 0-12 5.373-12 12 0 5.302 3.438 9.8 8.207 11.387.599.111.793-.261.793-.577v-2.234c-3.338.726-4.033-1.416-4.033-1.416-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23.957-.266 1.983-.399 3.003-.404 1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222v3.293c0 .319.192.694.801.576 4.765-1.589 8.199-6.086 8.199-11.386 0-6.627-5.373-12-12-12z" />
          </svg>
          View on GitHub
        </a>
      </div>

      {/* Floating code snippet */}
      <div className="relative max-w-xl mx-auto">
        <div className="absolute -inset-1 bg-gradient-to-r from-[#00ADD8]/20 via-[#0077b6]/10 to-[#7c3aed]/20 rounded-2xl blur-xl" />
        <div className="relative rounded-2xl border border-white/[0.08] bg-[#0d0d14] p-6 text-left shadow-2xl">
          <div className="flex items-center gap-2 mb-4">
            <div className="w-3 h-3 rounded-full bg-[#ff5f57]" />
            <div className="w-3 h-3 rounded-full bg-[#febc2e]" />
            <div className="w-3 h-3 rounded-full bg-[#28c840]" />
            <span className="ml-3 text-xs text-[#64748b] font-mono">
              main.go
            </span>
          </div>
          <CodeBlock language="go" className="text-sm font-mono leading-relaxed overflow-x-auto m-0 bg-transparent border-none p-0 mb-0 shadow-none">
            {codeSnippet}
          </CodeBlock>
        </div>
      </div>
    </>
  );

  return (
    <section className="relative min-h-screen flex items-center justify-center px-4 sm:px-6 lg:px-8 overflow-hidden">
      <HeroBackground />
      <div className="relative z-10 text-center max-w-5xl mx-auto pt-24 pb-16">
        {shouldReduceMotion ? (
          content
        ) : (
          <motion.div
            variants={containerVariants}
            initial="hidden"
            animate="visible"
          >
            <motion.div variants={itemVariants}>
              <div className="mb-6">
                <span className="inline-flex items-center gap-2 rounded-full border border-[#00ADD8]/30 bg-[#00ADD8]/10 px-4 py-1.5 text-sm font-medium text-[#00ADD8] shadow-[0_0_20px_rgba(0,173,216,0.15)]">
                  <span className="h-2 w-2 rounded-full bg-[#00ADD8] animate-pulse" />
                  v1.0.0 — Production Ready
                </span>
              </div>
            </motion.div>

            <motion.h1
              variants={itemVariants}
              className="text-5xl sm:text-6xl md:text-7xl lg:text-8xl font-extrabold tracking-tight leading-[0.95] mb-6"
            >
              Event Sourcing
              <br />
              <GradientText>for Go</GradientText>
            </motion.h1>

            <motion.p
              variants={itemVariants}
              className="text-lg sm:text-xl md:text-2xl text-[#94a3b8] max-w-2xl mx-auto mb-10 leading-relaxed"
            >
              Built for developers who demand simplicity without sacrificing
              power. Production-ready event sourcing & CQRS toolkit.
            </motion.p>

            <motion.div
              variants={itemVariants}
              className="flex flex-col sm:flex-row items-center justify-center gap-4 mb-16"
            >
              <Link
                to="/docs/getting-started/introduction"
                className="group inline-flex items-center gap-2 rounded-xl bg-gradient-to-r from-[#00ADD8] to-[#0077b6] px-8 py-3.5 text-base font-semibold text-white shadow-lg shadow-[#00ADD8]/20 transition-all duration-200 hover:shadow-xl hover:shadow-[#00ADD8]/30 hover:-translate-y-0.5 no-underline hover:no-underline hover:text-white"
              >
                Get Started
                <span className="transition-transform duration-200 group-hover:translate-x-0.5">
                  &rarr;
                </span>
              </Link>
              <a
                href="https://github.com/AshkanYarmoradi/go-mink"
                className="inline-flex items-center gap-2 rounded-xl border border-white/10 bg-white/[0.03] px-8 py-3.5 text-base font-semibold text-[#e2e8f0] transition-all duration-200 hover:bg-white/[0.06] hover:border-white/20 no-underline hover:no-underline hover:text-white"
              >
                <svg
                  className="w-5 h-5"
                  fill="currentColor"
                  viewBox="0 0 24 24"
                >
                  <path d="M12 0c-6.626 0-12 5.373-12 12 0 5.302 3.438 9.8 8.207 11.387.599.111.793-.261.793-.577v-2.234c-3.338.726-4.033-1.416-4.033-1.416-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23.957-.266 1.983-.399 3.003-.404 1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222v3.293c0 .319.192.694.801.576 4.765-1.589 8.199-6.086 8.199-11.386 0-6.627-5.373-12-12-12z" />
                </svg>
                View on GitHub
              </a>
            </motion.div>

            <motion.div variants={itemVariants}>
              <div className="relative max-w-xl mx-auto">
                <div className="absolute -inset-1 bg-gradient-to-r from-[#00ADD8]/20 via-[#0077b6]/10 to-[#7c3aed]/20 rounded-2xl blur-xl" />
                <div className="relative rounded-2xl border border-white/[0.08] bg-[#0d0d14] p-6 text-left shadow-2xl">
                  <div className="flex items-center gap-2 mb-4">
                    <div className="w-3 h-3 rounded-full bg-[#ff5f57]" />
                    <div className="w-3 h-3 rounded-full bg-[#febc2e]" />
                    <div className="w-3 h-3 rounded-full bg-[#28c840]" />
                    <span className="ml-3 text-xs text-[#64748b] font-mono">
                      main.go
                    </span>
                  </div>
                  <CodeBlock language="go" className="text-sm font-mono leading-relaxed overflow-x-auto m-0 bg-transparent border-none p-0 mb-0 shadow-none">
                    {codeSnippet}
                  </CodeBlock>
                </div>
              </div>
            </motion.div>
          </motion.div>
        )}
      </div>
    </section>
  );
}
