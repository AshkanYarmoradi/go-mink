import React from "react";
import Layout from "@theme/Layout";
import Link from "@docusaurus/Link";
import {
  ArrowRight,
  ArrowUpRight,
  Blocks,
  Braces,
  Check,
  Database,
  GitBranch,
  Layers3,
  LockKeyhole,
  Terminal,
  Workflow,
} from "lucide-react";
import EventReplay from "../components/Landing/EventReplay";
import CopyCommand from "../components/Landing/CopyCommand";
import CodeExamples from "../components/Landing/CodeExamples";

const features = [
  {
    icon: Database,
    title: "A durable event history",
    text: "Append to streams with optimistic concurrency. Use PostgreSQL for persistence and memory for fast, isolated tests.",
    to: "/docs/core/event-store",
    label: "Explore the event store",
    className: "mk-feature-wide",
  },
  {
    icon: Layers3,
    title: "Views shaped for your queries",
    text: "Build read models from events. Choose inline, asynchronous, or live projections and wire the execution explicitly.",
    to: "/docs/core/read-models",
    label: "Understand projections",
    className: "mk-feature-wide",
  },
  {
    icon: Workflow,
    title: "Workflows across boundaries",
    text: "Coordinate multi-step processes with sagas. Publish committed events through a transactional outbox on supported adapters.",
    to: "/docs/advanced/advanced-patterns",
    label: "Connect your services",
  },
  {
    icon: LockKeyhole,
    title: "Data protection by design",
    text: "Field encryption, key management, and erasure tools help you implement your application’s data policies.",
    to: "/docs/advanced/security",
    label: "Explore data protection",
  },
  {
    icon: Braces,
    title: "Confidence at every change",
    text: "Describe behavior with Given / When / Then tests. Evolve event schemas with upcasters while keeping history readable.",
    to: "/docs/guide/testing",
    label: "Test your domain",
  },
];
export default function Home() {
  return (
    <Layout
      title="Event sourcing & CQRS for Go"
      description="Build Go applications that remember. An open-source event sourcing and CQRS toolkit with PostgreSQL persistence, explicit projections, sagas, and practical testing tools."
      wrapperClassName="landing-page"
    >
      <main className="mk-home">
        <section className="mk-hero" aria-labelledby="hero-title">
          <div className="mk-hero-grid" aria-hidden="true" />
          <div className="mk-container mk-hero-content">
            <div className="mk-hero-copy">
              <a
                className="mk-release"
                href="https://github.com/AshkanYarmoradi/go-mink/releases"
              >
                <span className="mk-status-dot" /> OPEN SOURCE. BUILT FOR GO.{" "}
                <ArrowUpRight size={13} />
              </a>
              <h1 id="hero-title">
                Every event.
                <br />A clearer <span>picture.</span>
              </h1>
              <p className="mk-hero-description">
                Event sourcing & CQRS for Go.
                <br />
                Keep the history. Rebuild the state. Give your application a
                memory you can work with.
              </p>
              <div className="mk-actions">
                <Link
                  className="mk-button mk-button-primary"
                  to="/docs/getting-started/introduction"
                >
                  Start building <ArrowRight size={17} />
                </Link>
                <a
                  className="mk-button mk-button-secondary"
                  href="https://github.com/AshkanYarmoradi/go-mink"
                >
                  <GitBranch size={17} /> View source
                </a>
              </div>
              <CopyCommand />
            </div>
            <div className="mk-hero-workbench">
              <div className="mk-workbench-label">
                <span>01—04</span> FROM HISTORY TO STATE{" "}
                <span className="mk-label-line" />
              </div>
              <EventReplay />
              <p className="mk-workbench-hint">
                Go back in time. Select an event or hit replay.
              </p>
            </div>
          </div>
          <div className="mk-container mk-foundations">
            <span>A toolkit that fits your stack</span>
            <div>
              <Braces size={18} />
              <strong>Idiomatic Go</strong>
            </div>
            <div>
              <Database size={18} />
              <strong>PostgreSQL</strong>
            </div>
            <div>
              <GitBranch size={18} />
              <strong>CQRS</strong>
            </div>
            <div>
              <Blocks size={18} />
              <strong>Open interfaces</strong>
            </div>
            <a href="https://github.com/AshkanYarmoradi/go-mink/blob/main/LICENSE">
              Apache 2.0 <ArrowUpRight size={13} />
            </a>
          </div>
        </section>
        <div className="mk-container">
          <section
            className="mk-section mk-story"
            id="how-it-works"
            aria-labelledby="story-title"
          >
            <div className="mk-section-heading">
              <div>
                <span className="mk-eyebrow">
                  01 / A DIFFERENT WAY TO THINK ABOUT STATE
                </span>
                <h2 id="story-title">
                  Keep the story.
                  <br />
                  <span className="mk-muted">Build any view.</span>
                </h2>
              </div>
              <p>
                A row tells you what is true now. An event stream tells you how
                you got here. go-mink gives you the building blocks to use both.
              </p>
            </div>
            <div className="mk-pipeline">
              <article>
                <span className="mk-step">01</span>
                <div
                  className="mk-pipeline-art mk-intent-art"
                  aria-hidden="true"
                >
                  <span className="mk-mini-command">
                    ShipOrder <ArrowRight size={14} />
                  </span>
                  <span className="mk-mini-validation">
                    <Check size={12} /> Validate the intent
                  </span>
                </div>
                <h3>Express an intention</h3>
                <p>
                  Commands capture what should happen. Your domain decides
                  whether it can.
                </p>
                <Link to="/docs/advanced/advanced-patterns">
                  Commands & aggregates <ArrowUpRight size={14} />
                </Link>
              </article>
              <article>
                <span className="mk-step">02</span>
                <div
                  className="mk-pipeline-art mk-history-art"
                  aria-hidden="true"
                >
                  <span />
                  <span />
                  <span>
                    OrderShipped <b>v4</b>
                  </span>
                </div>
                <h3>Record what happened</h3>
                <p>
                  Append a fact to the stream. Version checks help protect
                  against conflicting writes.
                </p>
                <Link to="/docs/core/event-store">
                  Streams & concurrency <ArrowUpRight size={14} />
                </Link>
              </article>
              <article>
                <span className="mk-step">03</span>
                <div className="mk-pipeline-art mk-view-art" aria-hidden="true">
                  <i />
                  <i />
                  <i />
                  <div>
                    <span>orders_by_status</span>
                    <b>
                      Shipped <Check size={12} />
                    </b>
                  </div>
                </div>
                <h3>Project what you need</h3>
                <p>
                  Turn the same events into useful views. Replay history when
                  your questions change.
                </p>
                <Link to="/docs/core/read-models">
                  Read models & projections <ArrowUpRight size={14} />
                </Link>
              </article>
            </div>
          </section>
          <CodeExamples />
          <section
            className="mk-section"
            id="toolkit"
            aria-labelledby="toolkit-title"
          >
            <div className="mk-section-heading">
              <div>
                <span className="mk-eyebrow">
                  03 / SMALL PIECES. A COMPLETE TOOLKIT.
                </span>
                <h2 id="toolkit-title">
                  Built for the parts
                  <br />
                  beyond the happy path.
                </h2>
              </div>
              <p>
                Persistence is just the beginning. Compose the pieces you need
                for a system that evolves with your domain.
              </p>
            </div>
            <div className="mk-feature-grid">
              {features.map(({ icon: Icon, ...feature }, index) => (
                <article
                  key={feature.title}
                  className={`mk-feature ${feature.className || ""}`}
                >
                  <div className="mk-feature-top">
                    <Icon size={23} strokeWidth={1.5} />
                    <span>0{index + 1}</span>
                  </div>
                  <h3>{feature.title}</h3>
                  <p>{feature.text}</p>
                  <Link to={feature.to}>
                    {feature.label} <ArrowUpRight size={15} />
                  </Link>
                </article>
              ))}
            </div>
            <div className="mk-benchmark-link">
              <span>
                <Terminal size={17} /> Evaluate it on your workload.
              </span>
              <Link to="/docs/guide/benchmarks">
                Read the benchmark methodology <ArrowUpRight size={16} />
              </Link>
            </div>
          </section>
          <section
            className="mk-section mk-learn"
            id="learn"
            aria-labelledby="learn-title"
          >
            <div className="mk-section-heading">
              <div>
                <span className="mk-eyebrow">
                  04 / YOUR NEXT COMMIT STARTS HERE
                </span>
                <h2 id="learn-title">
                  A path from curious
                  <br />
                  to shipping.
                </h2>
              </div>
              <Link
                className="mk-text-link"
                to="/docs/getting-started/introduction"
              >
                Browse the documentation <ArrowRight size={17} />
              </Link>
            </div>
            <div className="mk-learning-grid">
              <Link className="mk-learning-card" to="/docs/tutorial/setup">
                <span className="mk-learning-number">01 / BUILD</span>
                <h3>Learn by doing.</h3>
                <p>
                  Build an e-commerce application, from your first event to
                  production concerns.
                </p>
                <span className="mk-learning-bottom">
                  Follow the tutorial <ArrowUpRight size={20} />
                </span>
              </Link>
              <Link
                className="mk-learning-card"
                to="/docs/blog-series/introduction"
              >
                <span className="mk-learning-number">02 / UNDERSTAND</span>
                <h3>Get the mental model.</h3>
                <p>
                  Work through event sourcing, CQRS, middleware, and projections
                  in the article series.
                </p>
                <span className="mk-learning-bottom">
                  Read the series <ArrowUpRight size={20} />
                </span>
              </Link>
              <Link className="mk-learning-card" to="/docs/advanced/api-design">
                <span className="mk-learning-number">03 / EXPLORE</span>
                <h3>Go a level deeper.</h3>
                <p>
                  Find the interfaces, patterns, and design decisions behind the
                  toolkit.
                </p>
                <span className="mk-learning-bottom">
                  Explore the API <ArrowUpRight size={20} />
                </span>
              </Link>
            </div>
          </section>
          <section className="mk-section mk-faq" aria-labelledby="faq-title">
            <div>
              <span className="mk-eyebrow">BEFORE YOU BUILD</span>
              <h2 id="faq-title">
                Good questions.
                <br />
                Straight answers.
              </h2>
              <p>
                Event sourcing is a design choice.
                <br />
                Make it an informed one.
              </p>
            </div>
            <div className="mk-faq-list">
              <details>
                <summary>
                  Is event sourcing right for my application?<span>+</span>
                </summary>
                <p>
                  It is useful when history, traceability, rebuilding views, and
                  complex domain behavior matter. It also adds schema evolution
                  and operational responsibilities. For straightforward CRUD, a
                  conventional database model may be simpler.{" "}
                  <Link to="/docs/getting-started/architecture">
                    Read the architecture guide.
                  </Link>
                </p>
              </details>
              <details>
                <summary>
                  Do I need a separate event database?<span>+</span>
                </summary>
                <p>
                  No. The PostgreSQL adapter stores events in PostgreSQL. The
                  memory adapter is useful for tests and local experiments; it
                  is not durable storage.{" "}
                  <Link to="/docs/core/adapters">Explore adapters.</Link>
                </p>
              </details>
              <details>
                <summary>
                  Do projections run automatically on append?<span>+</span>
                </summary>
                <p>
                  No. Appending an event and processing a projection are
                  separate operations. Configure the projection engine and
                  invoke inline processing, run asynchronous workers, or notify
                  live projections according to your application’s needs.{" "}
                  <Link to="/docs/core/read-models">
                    Understand projection strategies.
                  </Link>
                </p>
              </details>
              <details>
                <summary>
                  Can I evolve events and protect personal data?<span>+</span>
                </summary>
                <p>
                  Upcasters help read older event schemas. Field encryption,
                  erasure, and export capabilities support your data governance
                  design; your retention policy and compliance obligations still
                  need application-specific decisions.{" "}
                  <Link to="/docs/advanced/security">
                    Read the security guide.
                  </Link>
                </p>
              </details>
            </div>
          </section>
          <section className="mk-final-cta" aria-labelledby="cta-title">
            <div className="mk-cta-symbol" aria-hidden="true">
              <GitBranch size={34} />
            </div>
            <span className="mk-eyebrow">YOUR NEXT EVENT STARTS HERE</span>
            <h2 id="cta-title">
              Build something
              <br />
              worth remembering.
            </h2>
            <p>Open source. Plain Go. Ready for your domain.</p>
            <div className="mk-actions">
              <Link
                className="mk-button mk-button-primary"
                to="/docs/getting-started/introduction"
              >
                Get started with go-mink <ArrowRight size={17} />
              </Link>
              <a
                className="mk-button mk-button-secondary"
                href="https://github.com/AshkanYarmoradi/go-mink/discussions"
              >
                Join the discussion <ArrowUpRight size={16} />
              </a>
            </div>
          </section>
        </div>
      </main>
    </Layout>
  );
}
