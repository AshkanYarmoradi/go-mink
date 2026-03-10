import React from "react";
import { motion, useReducedMotion } from "framer-motion";

interface GlassCardProps {
  children: React.ReactNode;
  className?: string;
  hoverGlow?: boolean;
}

export default function GlassCard({
  children,
  className = "",
  hoverGlow = true,
}: GlassCardProps) {
  const shouldReduceMotion = useReducedMotion();

  const baseClasses =
    "relative rounded-2xl bg-[rgba(26,26,36,0.6)] backdrop-blur-xl border border-white/[0.08] overflow-hidden";

  if (shouldReduceMotion || !hoverGlow) {
    return <div className={`${baseClasses} ${className}`}>{children}</div>;
  }

  return (
    <motion.div
      className={`${baseClasses} transition-shadow duration-300 ${className}`}
      whileHover={{
        y: -4,
        boxShadow: "0 8px 40px rgba(0, 173, 216, 0.12)",
        borderColor: "rgba(0, 173, 216, 0.2)",
      }}
      transition={{ duration: 0.2 }}
    >
      {children}
    </motion.div>
  );
}
