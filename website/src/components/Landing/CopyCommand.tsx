import React, { useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";
export default function CopyCommand() {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  async function copy() {
    clearTimeout(timer.current);
    try {
      await navigator.clipboard.writeText("go get go-mink.dev");
      setState("copied");
    } catch {
      setState("failed");
    }
    timer.current = setTimeout(() => setState("idle"), 3500);
  }
  return (
    <div className="mk-install-wrap">
      <div className="mk-install">
        <span aria-hidden="true">$</span>
        <code>go get go-mink.dev</code>
        <button
          type="button"
          onClick={copy}
          aria-label="Copy installation command"
        >
          {state === "copied" ? <Check size={16} /> : <Copy size={16} />}
        </button>
      </div>
      <span className="mk-copy-status" role="status">
        {state === "copied"
          ? "Copied to clipboard"
          : state === "failed"
            ? "Select and copy the command above."
            : "Go 1.26+ · Apache 2.0"}
      </span>
    </div>
  );
}
