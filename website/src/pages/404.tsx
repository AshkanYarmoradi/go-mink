import React from "react";
import Layout from "@theme/Layout";
import Link from "@docusaurus/Link";
import GradientText from "../components/shared/GradientText";

export default function NotFound(): React.ReactElement {
  return (
    <Layout title="Page Not Found">
      <main className="flex items-center justify-center min-h-[70vh] px-4">
        <div className="text-center">
          <h1 className="text-8xl sm:text-9xl font-extrabold mb-4">
            <GradientText>404</GradientText>
          </h1>
          <p className="text-xl text-[#94a3b8] mb-8">
            This page doesn't exist — but your events are safe.
          </p>
          <div className="flex flex-col sm:flex-row items-center justify-center gap-4">
            <Link
              to="/"
              className="inline-flex items-center gap-2 rounded-xl bg-gradient-to-r from-[#00ADD8] to-[#0077b6] px-8 py-3.5 text-base font-semibold text-white shadow-lg shadow-[#00ADD8]/20 transition-all duration-200 hover:shadow-xl hover:-translate-y-0.5 no-underline hover:no-underline hover:text-white"
            >
              Back to Home
            </Link>
            <Link
              to="/docs/getting-started/introduction"
              className="inline-flex items-center gap-2 rounded-xl border border-white/10 bg-white/[0.03] px-8 py-3.5 text-base font-semibold text-[#e2e8f0] transition-all duration-200 hover:bg-white/[0.06] hover:border-white/20 no-underline hover:no-underline hover:text-white"
            >
              Read the Docs
            </Link>
          </div>
        </div>
      </main>
    </Layout>
  );
}
