import React, { useRef, useState } from "react";
import Link from "@docusaurus/Link";
import CodeBlock from "@theme/CodeBlock";
import { ArrowUpRight, FileCode2 } from "lucide-react";
import { examples } from "../../data/examples";
export default function CodeExamples() {
  const [active, setActive] = useState(0);
  const tabs = useRef<(HTMLButtonElement | null)[]>([]);
  const example = examples[active];
  return (
    <section
      className="mk-section mk-code-section"
      id="code"
      aria-labelledby="code-title"
    >
      <div className="mk-code-intro">
        <span className="mk-eyebrow">02 / THE DEVELOPER EXPERIENCE</span>
        <h2 id="code-title">
          Your domain.
          <br />
          Plain Go.
        </h2>
        <p>
          Start with an event and a stream. Add the patterns your application
          needs, when it needs them.
        </p>
        <ul className="mk-check-list">
          <li>Interfaces you can implement</li>
          <li>Errors you can handle</li>
          <li>Context you can carry through</li>
        </ul>
        <Link className="mk-text-link" to="/docs/getting-started/introduction">
          Start building <ArrowUpRight size={17} />
        </Link>
      </div>
      <div className="mk-code-window">
        <div
          className="mk-code-tabs"
          role="tablist"
          aria-label="Go code examples"
        >
          {examples.map((item, index) => (
            <button
              key={item.id}
              id={`code-tab-${item.id}`}
              ref={(element) => {
                tabs.current[index] = element;
              }}
              type="button"
              role="tab"
              aria-selected={index === active}
              aria-controls="code-panel"
              tabIndex={index === active ? 0 : -1}
              onClick={() => setActive(index)}
              onKeyDown={(event) => {
                let next = index;
                if (event.key === "ArrowRight")
                  next = (index + 1) % examples.length;
                else if (event.key === "ArrowLeft")
                  next = (index - 1 + examples.length) % examples.length;
                else if (event.key === "Home") next = 0;
                else if (event.key === "End") next = examples.length - 1;
                else return;
                event.preventDefault();
                setActive(next);
                tabs.current[next]?.focus();
              }}
            >
              {String(index + 1).padStart(2, "0")} <span>{item.title}</span>
            </button>
          ))}
        </div>
        <div
          role="tabpanel"
          id="code-panel"
          aria-labelledby={`code-tab-${example.id}`}
          tabIndex={0}
        >
          <div className="mk-code-file">
            <FileCode2 size={14} />
            {example.filename}
            <span>GO</span>
          </div>
          <CodeBlock language="go">{example.code}</CodeBlock>
          <div className="mk-code-note">
            <p>{example.description}</p>
            <Link to={example.href}>
              View complete example <ArrowUpRight size={14} />
            </Link>
          </div>
        </div>
      </div>
    </section>
  );
}
