import React, { useEffect, useMemo, useRef, useState } from "react";
import Link from "@docusaurus/Link";
import useBaseUrl from "@docusaurus/useBaseUrl";
import { useHistory } from "@docusaurus/router";
import {
  Search,
  ArrowUpRight,
  ArrowDown,
  CornerDownLeft,
  X,
  BookOpen,
} from "lucide-react";
import {
  searchDocuments,
  excerpt,
} from "../../../plugins/local-search/search.cjs";

type SearchDocument = {
  title: string;
  url: string;
  section: string;
  description: string;
  text: string;
};
let cachedIndex: SearchDocument[] | undefined;
let indexRequest: Promise<SearchDocument[]> | undefined;

function loadIndex(url: string) {
  if (cachedIndex) return Promise.resolve(cachedIndex);
  if (!indexRequest) {
    indexRequest = fetch(url)
      .then((response) => {
        if (!response.ok) throw new Error("Search index unavailable");
        return response.json();
      })
      .then((data: SearchDocument[]) => {
        if (!Array.isArray(data)) throw new Error("Invalid search index");
        cachedIndex = data;
        return data;
      })
      .catch((error) => {
        indexRequest = undefined;
        throw error;
      });
  }
  return indexRequest;
}

const suggestions = [
  {
    title: "Start with your first event",
    url: "/docs/getting-started/introduction",
    section: "Getting started",
  },
  {
    title: "Build an order system",
    url: "/docs/tutorial/setup",
    section: "Guided tutorial",
  },
  {
    title: "Understand projections",
    url: "/docs/core/read-models",
    section: "Core concepts",
  },
];

export default function SearchBar() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [documents, setDocuments] = useState<SearchDocument[]>(
    cachedIndex || [],
  );
  const [status, setStatus] = useState<"loading" | "ready" | "error">(
    cachedIndex ? "ready" : "loading",
  );
  const [active, setActive] = useState(0);
  const [shortcut, setShortcut] = useState("Ctrl K");
  const dialog = useRef<HTMLDialogElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const previousFocus = useRef<HTMLElement | null>(null);
  const indexUrl = useBaseUrl("/search-index.json");
  const history = useHistory();
  const results = useMemo(
    () => searchDocuments(documents, query) as SearchDocument[],
    [documents, query],
  );
  const items = query.trim() ? results : suggestions;

  useEffect(() => {
    setShortcut(/Mac|iPhone|iPad/.test(navigator.platform) ? "⌘ K" : "Ctrl K");
    const handler = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setOpen((value) => !value);
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, []);

  useEffect(() => {
    if (!open) return;
    previousFocus.current = document.activeElement as HTMLElement;
    dialog.current?.showModal();
    input.current?.focus();
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    let mounted = true;
    setStatus(cachedIndex ? "ready" : "loading");
    loadIndex(indexUrl)
      .then((data) => {
        if (mounted) {
          setDocuments(data);
          setStatus("ready");
        }
      })
      .catch(() => {
        if (mounted) setStatus("error");
      });
    return () => {
      mounted = false;
      dialog.current?.close();
      document.body.style.overflow = previousOverflow;
      previousFocus.current?.focus();
    };
  }, [open, indexUrl]);

  useEffect(() => {
    setActive(0);
  }, [query]);
  useEffect(() => {
    if (open)
      document
        .getElementById(`mink-search-option-${active}`)
        ?.scrollIntoView({ block: "nearest" });
  }, [active, open]);

  function close() {
    setOpen(false);
  }
  function navigate(url: string) {
    close();
    history.push(url);
  }

  return (
    <>
      <button
        type="button"
        className="mk-search-trigger"
        onClick={() => setOpen(true)}
        aria-label="Search documentation"
        aria-keyshortcuts="Control+K Meta+K"
      >
        <Search size={16} aria-hidden="true" />
        <span>Search docs</span>
        <kbd>{shortcut}</kbd>
      </button>
      <dialog
        ref={dialog}
        className="mk-search-dialog"
        aria-labelledby="mink-search-title"
        onCancel={(event) => {
          event.preventDefault();
          close();
        }}
        onClick={(event) => {
          if (event.target === event.currentTarget) close();
        }}
      >
        <div className="mk-search-surface">
          <div className="mk-search-heading">
            <span id="mink-search-title">Search the documentation</span>
            <button type="button" onClick={close} aria-label="Close search">
              <X size={20} />
            </button>
          </div>
          <div className="mk-search-input-wrap">
            <Search size={22} aria-hidden="true" />
            <input
              ref={input}
              type="search"
              placeholder="Try projections, outbox, concurrency…"
              value={query}
              aria-label="Search documentation"
              role="combobox"
              aria-autocomplete="list"
              aria-expanded={true}
              aria-controls="mink-search-results"
              aria-activedescendant={
                items.length ? `mink-search-option-${active}` : undefined
              }
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  event.preventDefault();
                  event.stopPropagation();
                  close();
                  return;
                }
                if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                  event.preventDefault();
                  setActive((value) =>
                    items.length
                      ? (value +
                          (event.key === "ArrowDown" ? 1 : -1) +
                          items.length) %
                        items.length
                      : 0,
                  );
                }
                if (event.key === "Enter" && items[active]) {
                  event.preventDefault();
                  navigate(items[active].url);
                }
              }}
            />
          </div>
          <div className="mk-search-results-wrap">
            <div className="mk-search-label" aria-live="polite">
              {!query.trim()
                ? "Good places to begin"
                : status === "loading"
                  ? "Loading documentation…"
                  : status === "error"
                    ? "Search is temporarily unavailable"
                    : `${results.length} matching ${results.length === 1 ? "page" : "pages"}`}
            </div>
            {query.trim() && status === "ready" && !results.length && (
              <div className="mk-search-empty">
                <BookOpen size={28} aria-hidden="true" />
                <p>No pages match “{query}”.</p>
                <span>Try a broader term, such as events or projections.</span>
              </div>
            )}
            {status === "error" && (
              <p className="mk-search-error">
                We couldn’t load the search index. Close and reopen search to
                retry, or{" "}
                <Link to="/docs/getting-started/introduction" onClick={close}>
                  browse the docs
                </Link>
                .
              </p>
            )}
            <ul
              role="listbox"
              id="mink-search-results"
              aria-label="Documentation results"
            >
              {items.map((item, index) => (
                <li key={item.url} role="presentation">
                  <Link
                    to={item.url}
                    id={`mink-search-option-${index}`}
                    role="option"
                    aria-selected={active === index}
                    tabIndex={-1}
                    className="mk-search-result"
                    onClick={close}
                    onMouseEnter={() => setActive(index)}
                    onFocus={() => setActive(index)}
                  >
                    <div>
                      <span className="mk-search-section">{item.section}</span>
                      <strong>{item.title}</strong>
                      {"text" in item && <p>{excerpt(item.text, query)}</p>}
                    </div>
                    <ArrowUpRight size={20} aria-hidden="true" />
                  </Link>
                </li>
              ))}
            </ul>
          </div>
          <div className="mk-search-footer">
            <span>
              <ArrowDown size={13} aria-hidden="true" /> navigate{" "}
              <CornerDownLeft size={13} aria-hidden="true" /> open{" "}
              <kbd>esc</kbd> close
            </span>
            <span>Local search · no tracking</span>
          </div>
        </div>
      </dialog>
    </>
  );
}
