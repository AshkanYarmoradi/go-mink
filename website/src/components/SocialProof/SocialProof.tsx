import React from "react";
import SectionReveal from "../shared/SectionReveal";
import AnimatedCounter from "../shared/AnimatedCounter";
import GlassCard from "../shared/GlassCard";

const stats = [
  { value: 15, suffix: "+", label: "Features" },
  { value: 90, suffix: "%+", label: "Test Coverage" },
  { value: 2.5, suffix: "M", label: "Events/sec", isDecimal: true },
];

export default function SocialProof() {
  return (
    <section className="relative py-16 px-4 sm:px-6 lg:px-8">
      <div className="max-w-5xl mx-auto">
        <SectionReveal>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-6">
            {stats.map((stat, i) => (
              <GlassCard key={i} className="p-6 text-center">
                <div className="text-4xl sm:text-5xl font-extrabold text-white mb-2">
                  {stat.isDecimal ? (
                    <span>
                      2.5<span className="text-[#00ADD8]">M</span>
                    </span>
                  ) : (
                    <AnimatedCounter
                      target={stat.value}
                      suffix={stat.suffix}
                      className="text-white"
                    />
                  )}
                </div>
                <div className="text-sm text-[#94a3b8] font-medium uppercase tracking-wider">
                  {stat.label}
                </div>
              </GlassCard>
            ))}
          </div>
        </SectionReveal>

        {/* Placeholder for future company logos */}
        <SectionReveal delay={0.2}>
          <div className="mt-12 flex items-center justify-center gap-8 opacity-30">
            {[1, 2, 3, 4, 5].map((i) => (
              <div
                key={i}
                className="w-24 h-8 rounded bg-white/[0.05] border border-white/[0.04]"
              />
            ))}
          </div>
          <p className="text-center text-xs text-[#64748b] !mt-4">
            Trusted by teams building event-driven systems
          </p>
        </SectionReveal>
      </div>
    </section>
  );
}
