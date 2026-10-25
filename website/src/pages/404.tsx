import React from "react";
import Layout from "@theme/Layout";
import Head from "@docusaurus/Head";
import Link from "@docusaurus/Link";
import { ArrowRight, GitBranch } from "lucide-react";
export default function NotFound() {
  return (
    <Layout title="Page not found">
      <Head>
        <meta name="robots" content="noindex, follow" />
      </Head>
      <main className="mk-not-found">
        <GitBranch size={40} />
        <span className="mk-eyebrow">404 / STREAM NOT FOUND</span>
        <h1>This path has no events.</h1>
        <p>
          The page may have moved. Find your way back to the toolkit or search
          the documentation above.
        </p>
        <div className="mk-actions">
          <Link className="mk-button mk-button-primary" to="/">
            Back to go-mink <ArrowRight size={16} />
          </Link>
          <Link
            className="mk-button mk-button-secondary"
            to="/docs/getting-started/introduction"
          >
            Read the docs
          </Link>
        </div>
      </main>
    </Layout>
  );
}
