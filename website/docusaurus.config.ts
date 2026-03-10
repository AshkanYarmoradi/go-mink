import { themes as prismThemes } from "prism-react-renderer";
import type { Config } from "@docusaurus/types";
import type * as Preset from "@docusaurus/preset-classic";

const config: Config = {
  title: "go-mink",
  tagline: "Event Sourcing & CQRS Toolkit for Go",
  favicon: "img/favicon.svg",

  url: "https://go-mink.dev",
  baseUrl: "/",

  organizationName: "AshkanYarmoradi",
  projectName: "go-mink",

  onBrokenLinks: "throw",
  onBrokenMarkdownLinks: "warn",

  markdown: {
    format: "detect",
  },

  i18n: {
    defaultLocale: "en",
    locales: ["en"],
  },

  headTags: [
    {
      tagName: "link",
      attributes: {
        rel: "preconnect",
        href: "https://fonts.googleapis.com",
      },
    },
    {
      tagName: "link",
      attributes: {
        rel: "preconnect",
        href: "https://fonts.gstatic.com",
        crossorigin: "anonymous",
      },
    },
    {
      tagName: "link",
      attributes: {
        rel: "stylesheet",
        href: "https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800;900&family=JetBrains+Mono:wght@400;500;600;700&display=swap",
      },
    },
  ],

  presets: [
    [
      "classic",
      {
        docs: {
          sidebarPath: "./sidebars.ts",
          editUrl: "https://github.com/AshkanYarmoradi/go-mink/tree/main/website/",
        },
        blog: false,
        theme: {
          customCss: "./src/css/custom.css",
        },
      } satisfies Preset.Options,
    ],
  ],

  plugins: [
    [
      "@docusaurus/plugin-client-redirects",
      {
        redirects: [
          { from: "/docs/introduction", to: "/docs/getting-started/introduction" },
          { from: "/docs/architecture", to: "/docs/getting-started/architecture" },
          { from: "/blog", to: "/docs/blog-series/introduction" },
          { from: "/blog/01-introduction", to: "/docs/blog-series/introduction" },
          { from: "/blog/02-getting-started", to: "/docs/blog-series/getting-started" },
          { from: "/blog/03-aggregates", to: "/docs/blog-series/aggregates" },
          { from: "/blog/04-event-store", to: "/docs/blog-series/event-store" },
          { from: "/blog/05-cqrs", to: "/docs/blog-series/cqrs" },
          { from: "/blog/06-middleware", to: "/docs/blog-series/middleware" },
          { from: "/blog/07-projections", to: "/docs/blog-series/projections" },
          { from: "/blog/08-production", to: "/docs/blog-series/production" },
          { from: "/tutorial", to: "/docs/tutorial/setup" },
          { from: "/tutorial/01-setup", to: "/docs/tutorial/setup" },
          { from: "/tutorial/02-domain-modeling", to: "/docs/tutorial/domain-modeling" },
          { from: "/tutorial/03-commands-cqrs", to: "/docs/tutorial/commands-cqrs" },
          { from: "/tutorial/04-projections", to: "/docs/tutorial/projections" },
          { from: "/tutorial/05-testing", to: "/docs/tutorial/testing" },
          { from: "/tutorial/06-production", to: "/docs/tutorial/production" },
        ],
      },
    ],
  ],

  themeConfig: {
    image: "img/og-image.svg",
    colorMode: {
      defaultMode: "dark",
      disableSwitch: true,
      respectPrefersColorScheme: false,
    },
    navbar: {
      title: "go-mink",
      logo: {
        alt: "go-mink Logo",
        src: "img/logo.svg",
      },
      items: [
        {
          type: "docSidebar",
          sidebarId: "docs",
          position: "left",
          label: "Docs",
        },
        {
          type: "docSidebar",
          sidebarId: "tutorial",
          position: "left",
          label: "Tutorial",
        },
        {
          type: "docSidebar",
          sidebarId: "blogSeries",
          position: "left",
          label: "Blog Series",
        },
        {
          href: "https://github.com/AshkanYarmoradi/go-mink",
          label: "GitHub",
          position: "right",
        },
      ],
    },
    footer: {
      style: "dark",
      links: [
        {
          title: "Docs",
          items: [
            { label: "Introduction", to: "/docs/getting-started/introduction" },
            { label: "Architecture", to: "/docs/getting-started/architecture" },
            { label: "Event Store", to: "/docs/core/event-store" },
            { label: "API Reference", to: "/docs/advanced/api-design" },
          ],
        },
        {
          title: "Learn",
          items: [
            { label: "Tutorial", to: "/docs/tutorial/setup" },
            { label: "Blog Series", to: "/docs/blog-series/introduction" },
            { label: "Benchmarks", to: "/docs/guide/benchmarks" },
          ],
        },
        {
          title: "Community",
          items: [
            {
              label: "GitHub",
              href: "https://github.com/AshkanYarmoradi/go-mink",
            },
            {
              label: "Discussions",
              href: "https://github.com/AshkanYarmoradi/go-mink/discussions",
            },
            {
              label: "Issues",
              href: "https://github.com/AshkanYarmoradi/go-mink/issues",
            },
          ],
        },
      ],
      copyright: `Copyright &copy; ${new Date().getFullYear()} go-mink. Apache 2.0 License.`,
    },
    prism: {
      theme: prismThemes.nightOwl,
      darkTheme: prismThemes.nightOwl,
      additionalLanguages: ["go", "bash", "sql", "json", "yaml", "protobuf"],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
