const sectionLabels = {
  "getting-started": "Getting started",
  core: "Core concepts",
  advanced: "Advanced",
  guide: "Developer guide",
  tutorial: "Tutorial",
  "blog-series": "Field notes",
  adr: "Architecture decisions",
};

function plainText(markdown) {
  return markdown
    .replace(/^\uFEFF/, "")
    .replace(/^---\s*\r?\n[\s\S]*?\r?\n---\s*\r?\n/, "")
    .replace(/```[^\n]*\n([\s\S]*?)```/g, "$1")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/<[^>]*>/g, " ")
    .replace(/(?:^|\n)\s*(?:#{1,6}|:::|\|[-:| ]+\|)[^\S\n]*/g, " ")
    .replace(/[*`|]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

function makeRecord(doc, markdown) {
  return {
    title: doc.title,
    url: doc.permalink,
    section: sectionLabels[doc.id.split("/")[0]] || "Documentation",
    description: doc.description || "",
    headings: (
      markdown.replace(/```[\s\S]*?```/g, "").match(/^#{1,6} .+$/gm) || []
    )
      .map(plainText)
      .join(" "),
    text: plainText(markdown),
  };
}

function termsFor(query) {
  return query.toLowerCase().trim().split(/\s+/).filter(Boolean).slice(0, 12);
}

function searchDocuments(documents, query, limit = 8) {
  const terms = termsFor(query);
  if (!terms.length) return [];
  return documents
    .map((document) => {
      const title = document.title.toLowerCase();
      const description = document.description.toLowerCase();
      const text = document.text.toLowerCase();
      const headings = (document.headings || "").toLowerCase();
      const section = document.section.toLowerCase();
      const all = `${title} ${description} ${section} ${text}`;
      if (!terms.every((term) => all.includes(term))) return null;
      const score =
        terms.reduce(
          (total, term) =>
            total +
            (title.includes(term) ? 20 : 0) +
            (description.includes(term) ? 5 : 0) +
            (headings.includes(term) ? 8 : 0) +
            (section.includes(term) ? 3 : 0) +
            Math.min(4, text.split(term).length - 1),
          0,
        ) + (section === "core concepts" ? 2 : 0);
      return { ...document, score };
    })
    .filter(Boolean)
    .sort((a, b) => b.score - a.score || a.title.localeCompare(b.title))
    .slice(0, limit);
}

function excerpt(text, query, length = 150) {
  const positions = termsFor(query)
    .map((term) => text.toLowerCase().indexOf(term))
    .filter((position) => position >= 0);
  const match = positions.length ? Math.min(...positions) : 0;
  const start = Math.max(0, match - 40);
  const chunk = text.slice(start, start + length);
  return `${start ? "…" : ""}${chunk}${start + length < text.length ? "…" : ""}`;
}

module.exports = { plainText, makeRecord, searchDocuments, excerpt };
