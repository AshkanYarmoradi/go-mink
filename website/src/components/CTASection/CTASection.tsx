import React, { useState } from "react";
import SectionReveal from "../shared/SectionReveal";
import GradientText from "../shared/GradientText";

export default function CTASection() {
  const [copied, setCopied] = useState(false);

  const installCmd = "go get github.com/AshkanYarmoradi/go-mink";

  const copyToClipboard = () => {
    navigator.clipboard.writeText(installCmd);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <section className="relative py-32 px-4 sm:px-6 lg:px-8 overflow-hidden">
      {/* Gradient background */}
      <div className="absolute inset-0">
        <div
          className="absolute inset-0"
          style={{
            background:
              "radial-gradient(ellipse at center, rgba(0,173,216,0.08) 0%, transparent 60%)",
          }}
        />
        <div className="absolute bottom-0 left-0 right-0 h-px bg-gradient-to-r from-transparent via-[#00ADD8]/20 to-transparent" />
      </div>

      <div className="relative max-w-3xl mx-auto text-center">
        <SectionReveal>
          <h2 className="text-4xl sm:text-5xl md:text-6xl font-extrabold text-white mb-6">
            Ready to <GradientText>get started</GradientText>?
          </h2>
          <p className="text-xl text-[#94a3b8] mb-10 max-w-xl !mx-auto">
            Add go-mink to your project and start building event-driven systems
            in minutes.
          </p>

          {/* Install command */}
          <div className="inline-flex items-center gap-3 rounded-xl border border-white/[0.08] bg-[#0d0d14] px-6 py-4 mb-10 shadow-2xl">
            <span className="text-[#64748b] font-mono text-sm select-none">
              $
            </span>
            <code className="text-[#e2e8f0] font-mono text-sm sm:text-base bg-transparent border-none p-0">
              {installCmd}
            </code>
            <button
              onClick={copyToClipboard}
              className="ml-2 p-1.5 rounded-lg text-[#64748b] hover:text-[#e2e8f0] hover:bg-white/[0.06] transition-colors border-none bg-transparent cursor-pointer"
              title="Copy"
            >
              {copied ? (
                <svg
                  className="w-4 h-4 text-emerald-400"
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
              ) : (
                <svg
                  className="w-4 h-4"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                  strokeWidth={1.5}
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    d="M15.666 3.888A2.25 2.25 0 0013.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 01-.75.75H9.75a.75.75 0 01-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 01-2.25 2.25H6.75A2.25 2.25 0 014.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 011.927-.184"
                  />
                </svg>
              )}
            </button>
          </div>

          {/* CTA buttons */}
          <div className="flex flex-col sm:flex-row items-center justify-center gap-4">
            <a
              href="/docs/getting-started/introduction"
              className="inline-flex items-center gap-2 rounded-xl bg-gradient-to-r from-[#00ADD8] to-[#0077b6] px-8 py-3.5 text-base font-semibold text-white shadow-lg shadow-[#00ADD8]/20 transition-all duration-200 hover:shadow-xl hover:shadow-[#00ADD8]/30 hover:-translate-y-0.5 no-underline hover:no-underline hover:text-white"
            >
              Read the Docs
            </a>
            <a
              href="/docs/tutorial/setup"
              className="inline-flex items-center gap-2 rounded-xl border border-white/10 bg-white/[0.03] px-8 py-3.5 text-base font-semibold text-[#e2e8f0] transition-all duration-200 hover:bg-white/[0.06] hover:border-white/20 no-underline hover:no-underline hover:text-white"
            >
              View Tutorial
            </a>
          </div>
        </SectionReveal>
      </div>
    </section>
  );
}
