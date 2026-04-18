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

        <SectionReveal delay={0.2}>
          <p className="text-center text-sm text-[#94a3b8] uppercase tracking-widest font-medium !mt-16 !mb-6">
            Trusted by
          </p>
          <div className="flex items-center justify-center gap-6">
            {[
              { name: "Lawyours", domain: "lawyours.ai" },
              { name: "HuisScan", domain: "huisscan.ai" },
            ].map((customer) => (
              <a
                key={customer.domain}
                href={`https://${customer.domain}`}
                target="_blank"
                rel="noopener noreferrer"
                className="group relative rounded-2xl bg-[rgba(26,26,36,0.6)] backdrop-blur-xl border border-white/[0.08] px-8 py-5 flex items-center gap-3 transition-all duration-300 hover:-translate-y-1 hover:shadow-[0_8px_40px_rgba(0,173,216,0.12)] hover:border-[#00ADD8]/20"
              >
                <div className="w-9 h-9 rounded-lg bg-gradient-to-br from-[#00ADD8]/20 to-[#7c3aed]/20 border border-white/[0.06] flex items-center justify-center">
                  <span className="text-sm font-bold text-[#00ADD8] group-hover:text-white transition-colors duration-300">
                    {customer.name[0]}
                  </span>
                </div>
                <div className="flex flex-col">
                  <span className="text-white font-semibold text-base group-hover:text-white transition-colors duration-300">
                    {customer.name}
                  </span>
                  <span className="text-xs text-[#64748b] group-hover:text-[#94a3b8] transition-colors duration-300">
                    {customer.domain}
                  </span>
                </div>
                <svg
                  className="w-4 h-4 text-[#64748b] group-hover:text-[#00ADD8] ml-2 transition-all duration-300 group-hover:translate-x-0.5"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={1.5}
                  viewBox="0 0 24 24"
                >
                  <path strokeLinecap="round" strokeLinejoin="round" d="M4.5 19.5l15-15m0 0H8.25m11.25 0v11.25" />
                </svg>
              </a>
            ))}
          </div>
        </SectionReveal>
      </div>
    </section>
  );
}
