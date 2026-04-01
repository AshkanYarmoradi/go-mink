import React from "react";
import { motion, useReducedMotion } from "framer-motion";
import SectionReveal from "../shared/SectionReveal";
import GradientText from "../shared/GradientText";

const rows = [
  {
    aspect: "Event Storage",
    traditional: "Custom implementation",
    gomink: "Built-in, optimized",
  },
  {
    aspect: "Projections",
    traditional: "Manual, error-prone",
    gomink: "Automatic, reliable",
  },
  {
    aspect: "Database Support",
    traditional: "Locked to one DB",
    gomink: "Swap adapters anytime",
  },
  {
    aspect: "Testing",
    traditional: "Complex setup",
    gomink: "BDD fixtures included",
  },
  {
    aspect: "Learning Curve",
    traditional: "Steep",
    gomink: "Gentle, familiar API",
  },
  {
    aspect: "Encryption",
    traditional: "DIY per-field logic",
    gomink: "AES-256-GCM built in",
  },
  {
    aspect: "GDPR",
    traditional: "Manual implementation",
    gomink: "Crypto-shredding + export",
  },
];

function CheckIcon() {
  return (
    <svg
      className="w-5 h-5 text-emerald-400 inline-block mr-2 flex-shrink-0"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M4.5 12.75l6 6 9-13.5"
      />
    </svg>
  );
}

function XIcon() {
  return (
    <svg
      className="w-5 h-5 text-red-400/60 inline-block mr-2 flex-shrink-0"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M6 18L18 6M6 6l12 12"
      />
    </svg>
  );
}

export default function ComparisonTable() {
  const shouldReduceMotion = useReducedMotion();

  return (
    <section className="relative py-24 px-4 sm:px-6 lg:px-8">
      <div className="max-w-4xl mx-auto">
        <SectionReveal>
          <div className="text-center mb-12">
            <h2 className="text-3xl sm:text-4xl md:text-5xl font-extrabold text-white mb-4">
              Why choose <GradientText>go-mink</GradientText>?
            </h2>
            <p className="text-lg text-[#94a3b8] max-w-xl !mx-auto">
              See how go-mink compares to building event sourcing from scratch.
            </p>
          </div>
        </SectionReveal>

        <SectionReveal delay={0.1}>
          <div className="overflow-x-auto">
            <table className="w-full border-collapse">
              <thead>
                <tr className="border-b border-white/[0.06]">
                  <th className="text-left py-4 px-6 text-sm font-semibold text-[#94a3b8] uppercase tracking-wider">
                    Aspect
                  </th>
                  <th className="text-left py-4 px-6 text-sm font-semibold text-[#94a3b8] uppercase tracking-wider">
                    Traditional
                  </th>
                  <th className="text-left py-4 px-6 text-sm font-semibold text-[#00ADD8] uppercase tracking-wider">
                    go-mink
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row, i) =>
                  shouldReduceMotion ? (
                    <tr
                      key={i}
                      className="border-b border-white/[0.04] hover:bg-white/[0.02] transition-colors"
                    >
                      <td className="py-4 px-6 text-sm font-semibold text-[#e2e8f0]">
                        {row.aspect}
                      </td>
                      <td className="py-4 px-6 text-sm text-[#64748b]">
                        <XIcon />
                        {row.traditional}
                      </td>
                      <td className="py-4 px-6 text-sm text-[#e2e8f0]">
                        <CheckIcon />
                        {row.gomink}
                      </td>
                    </tr>
                  ) : (
                    <motion.tr
                      key={i}
                      className="border-b border-white/[0.04] hover:bg-white/[0.02] transition-colors"
                      initial={{ opacity: 0, x: -10 }}
                      whileInView={{ opacity: 1, x: 0 }}
                      viewport={{ once: true }}
                      transition={{ duration: 0.3, delay: i * 0.06 }}
                    >
                      <td className="py-4 px-6 text-sm font-semibold text-[#e2e8f0]">
                        {row.aspect}
                      </td>
                      <td className="py-4 px-6 text-sm text-[#64748b]">
                        <XIcon />
                        {row.traditional}
                      </td>
                      <td className="py-4 px-6 text-sm text-[#e2e8f0]">
                        <CheckIcon />
                        {row.gomink}
                      </td>
                    </motion.tr>
                  ),
                )}
              </tbody>
            </table>
          </div>
        </SectionReveal>
      </div>
    </section>
  );
}
