# Local documentation search

The plugin reads Docusaurus's loaded document metadata and the corresponding Markdown files. It emits `search-index.json` through webpack in development and production, using each document's real permalink. Draft/unlisted documents do not enter the public index.

`src/theme/SearchBar` downloads the index only when search opens, caches it for the session, and searches it locally. Queries are not sent to a service. Results rank title, headings, description, section, and body matches; all query terms must match. A native dialog supplies modal focus containment. Arrow keys select a result; Enter opens it; Escape closes search and restores focus.

Run the search behavior checks:

```sh
node --test plugins/local-search/search.test.cjs
node scripts/verify-search.cjs # after npm run build
```

After a production build, verify `build/search-index.json` contains the expected document permalinks. Test search from both a documentation page and the homepage, including Ctrl/Cmd+K, no results, keyboard navigation, the mobile layout, and the network-error fallback. No hosted credentials or additional dependencies are needed.
