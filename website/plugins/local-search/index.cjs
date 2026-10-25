const fs = require("node:fs/promises");
const path = require("node:path");
const { makeRecord } = require("./search.cjs");

// Emit the same asset through webpack in development and production. The index
// uses Docusaurus permalinks instead of reconstructing paths from filenames.
module.exports = function localSearch(context) {
  let index = [];
  return {
    name: "mink-local-search",
    async allContentLoaded({ allContent }) {
      const instances = Object.values(
        allContent["docusaurus-plugin-content-docs"] || {},
      );
      const docs = instances.flatMap((instance) =>
        instance.loadedVersions.flatMap((version) => version.docs),
      );
      index = await Promise.all(
        docs
          .filter(
            (doc) => !doc.frontMatter?.unlisted && !doc.frontMatter?.draft,
          )
          .map(async (doc) => {
            const sourcePath = doc.source.replace(/^@site[\\/]/, "");
            const markdown = await fs.readFile(
              path.resolve(context.siteDir, sourcePath),
              "utf8",
            );
            return makeRecord(doc, markdown);
          }),
      );
    },
    configureWebpack(_config, isServer) {
      if (isServer) return {};
      return {
        plugins: [
          {
            apply(compiler) {
              compiler.hooks.thisCompilation.tap(
                "MinkLocalSearch",
                (compilation) => {
                  compilation.hooks.processAssets.tap(
                    {
                      name: "MinkLocalSearch",
                      stage:
                        compiler.webpack.Compilation
                          .PROCESS_ASSETS_STAGE_ADDITIONAL,
                    },
                    () => {
                      compilation.emitAsset(
                        "search-index.json",
                        new compiler.webpack.sources.RawSource(
                          JSON.stringify(index),
                        ),
                      );
                    },
                  );
                },
              );
            },
          },
        ],
      };
    },
  };
};
