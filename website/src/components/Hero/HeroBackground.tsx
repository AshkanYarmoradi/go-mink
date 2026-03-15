import React from "react";
import { motion, useReducedMotion } from "framer-motion";

export default function HeroBackground() {
  const shouldReduceMotion = useReducedMotion();

  return (
    <div className="absolute inset-0 overflow-hidden">
      {/* Grid pattern */}
      <div
        className="absolute inset-0 opacity-[0.03]"
        style={{
          backgroundImage: `
            repeating-linear-gradient(0deg, transparent, transparent 59px, rgba(255,255,255,0.5) 59px, rgba(255,255,255,0.5) 60px),
            repeating-linear-gradient(90deg, transparent, transparent 59px, rgba(255,255,255,0.5) 59px, rgba(255,255,255,0.5) 60px)
          `,
        }}
      />

      {/* Cyan glow orb */}
      {shouldReduceMotion ? (
        <div
          className="absolute w-[600px] h-[600px] rounded-full opacity-20"
          style={{
            background:
              "radial-gradient(circle, rgba(0,173,216,0.4) 0%, transparent 70%)",
            top: "10%",
            left: "20%",
          }}
        />
      ) : (
        <motion.div
          className="absolute w-[600px] h-[600px] rounded-full opacity-20"
          style={{
            background:
              "radial-gradient(circle, rgba(0,173,216,0.4) 0%, transparent 70%)",
          }}
          animate={{
            x: [0, 30, -20, 0],
            y: [0, -20, 15, 0],
          }}
          transition={{
            duration: 20,
            repeat: Infinity,
            ease: "linear",
          }}
          initial={{ top: "10%", left: "20%" }}
        />
      )}

      {/* Purple glow orb */}
      {shouldReduceMotion ? (
        <div
          className="absolute w-[500px] h-[500px] rounded-full opacity-15"
          style={{
            background:
              "radial-gradient(circle, rgba(124,58,237,0.4) 0%, transparent 70%)",
            top: "30%",
            right: "10%",
          }}
        />
      ) : (
        <motion.div
          className="absolute w-[500px] h-[500px] rounded-full opacity-15"
          style={{
            background:
              "radial-gradient(circle, rgba(124,58,237,0.4) 0%, transparent 70%)",
          }}
          animate={{
            x: [0, -25, 15, 0],
            y: [0, 20, -10, 0],
          }}
          transition={{
            duration: 25,
            repeat: Infinity,
            ease: "linear",
          }}
          initial={{ top: "30%", right: "10%" }}
        />
      )}

      {/* Gradient fade at bottom */}
      <div className="absolute bottom-0 left-0 right-0 h-32 bg-gradient-to-t from-[#0a0a0f] to-transparent" />
    </div>
  );
}
