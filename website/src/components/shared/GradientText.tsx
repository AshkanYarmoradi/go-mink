import React, { ElementType } from "react";

interface GradientTextProps {
  children: React.ReactNode;
  className?: string;
  as?: ElementType;
}

export default function GradientText({
  children,
  className = "",
  as: Component = "span",
}: GradientTextProps) {
  return (
    <Component
      className={`bg-gradient-to-r from-[#00ADD8] via-[#0077b6] to-[#7c3aed] bg-clip-text text-transparent ${className}`}
    >
      {children}
    </Component>
  );
}
