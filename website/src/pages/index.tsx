import React from "react";
import Layout from "@theme/Layout";
import Hero from "../components/Hero/Hero";
import SocialProof from "../components/SocialProof/SocialProof";
import FeatureGrid from "../components/FeatureGrid/FeatureGrid";
import CodeShowcase from "../components/CodeShowcase/CodeShowcase";
import ArchitectureDiagram from "../components/ArchitectureDiagram/ArchitectureDiagram";
import BenchmarkStats from "../components/BenchmarkStats/BenchmarkStats";
import ComparisonTable from "../components/ComparisonTable/ComparisonTable";
import CTASection from "../components/CTASection/CTASection";

export default function Home(): React.ReactElement {
  return (
    <Layout
      title="Event Sourcing & CQRS for Go"
      description="go-mink is a comprehensive Event Sourcing & CQRS toolkit for Go. Production-ready with PostgreSQL adapters, projections, sagas, encryption, and more."
      wrapperClassName="landing-page"
    >
      <main className="bg-[#0a0a0f] text-white overflow-hidden">
        <Hero />
        <SocialProof />
        <FeatureGrid />
        <CodeShowcase />
        <ArchitectureDiagram />
        <BenchmarkStats />
        <ComparisonTable />
        <CTASection />
      </main>
    </Layout>
  );
}
