import React from "react";
import SectionReveal from "../shared/SectionReveal";
import AnimatedCounter from "../shared/AnimatedCounter";
import GlassCard from "../shared/GlassCard";
import GradientText from "../shared/GradientText";

const benchmarks = [
  {
    value: 2.5,
    label: "Events/sec",
    suffix: "M",
    description: "In-memory throughput",
    isDecimal: true,
  },
  {
    value: 6,
    label: "Batch write",
    suffix: "M",
    description: "Events in a single batch",
    isDecimal: false,
  },
  {
    value: 30,
    label: "PostgreSQL ops/sec",
    suffix: "K",
    description: "Concurrent event writes",
    isDecimal: false,
  },
  {
    value: 32,
    label: "Workers",
    suffix: "",
    description: "Concurrent worker support",
    isDecimal: false,
  },
];

export default function BenchmarkStats() {
  return (
    <section className="relative py-24 px-4 sm:px-6 lg:px-8 overflow-hidden">
      {/* Gradient mesh background */}
      <div className="absolute inset-0 opacity-30">
        <div
          className="absolute w-[800px] h-[400px] top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 rounded-full"
          style={{
            background:
              "radial-gradient(ellipse, rgba(0,173,216,0.15) 0%, rgba(124,58,237,0.08) 40%, transparent 70%)",
          }}
        />
      </div>

      <div className="relative max-w-5xl mx-auto">
        <SectionReveal>
          <div className="text-center mb-16">
            <h2 className="text-3xl sm:text-4xl md:text-5xl font-extrabold text-white mb-4">
              <GradientText>Blazing fast</GradientText> performance
            </h2>
            <p className="text-lg text-[#94a3b8] max-w-xl !mx-auto">
              Benchmarked with real workloads. Optimized for production scale.
            </p>
          </div>
        </SectionReveal>

        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-6">
          {benchmarks.map((bench, i) => (
            <SectionReveal key={i} delay={i * 0.1}>
              <GlassCard className="p-6 text-center h-full">
                <div className="text-4xl sm:text-5xl font-extrabold text-white mb-1">
                  {bench.isDecimal ? (
                    <span>
                      2.5<span className="text-[#00ADD8]">{bench.suffix}</span>
                    </span>
                  ) : (
                    <>
                      <AnimatedCounter
                        target={bench.value}
                        className="text-white"
                      />
                      <span className="text-[#00ADD8]">{bench.suffix}</span>
                    </>
                  )}
                </div>
                <div className="text-base font-semibold text-[#e2e8f0] mb-1">
                  {bench.label}
                </div>
                <div className="text-xs text-[#64748b]">
                  {bench.description}
                </div>

                {/* Visual bar */}
                <div className="mt-4 h-1.5 bg-white/[0.04] rounded-full overflow-hidden">
                  <div
                    className="h-full rounded-full bg-gradient-to-r from-[#00ADD8] to-[#7c3aed]"
                    style={{
                      width: `${Math.min(((bench.value * (bench.suffix === "M" ? 1000000 : bench.suffix === "K" ? 1000 : 1)) / 6000000) * 100, 100)}%`,
                    }}
                  />
                </div>
              </GlassCard>
            </SectionReveal>
          ))}
        </div>
      </div>
    </section>
  );
}
