import React from "react";
import { motion, useReducedMotion } from "framer-motion";
import SectionReveal from "../shared/SectionReveal";
import GradientText from "../shared/GradientText";

interface DiagramNode {
  id: string;
  label: string;
  x: number;
  y: number;
  width: number;
  height: number;
  color: string;
}

interface DiagramConnection {
  from: string;
  to: string;
  label?: string;
}

const nodes: DiagramNode[] = [
  { id: "commands", label: "Commands", x: 40, y: 100, width: 120, height: 50, color: "#00ADD8" },
  { id: "bus", label: "Command Bus", x: 210, y: 100, width: 130, height: 50, color: "#0077b6" },
  { id: "handler", label: "Handler", x: 390, y: 100, width: 120, height: 50, color: "#00ADD8" },
  { id: "aggregate", label: "Aggregate", x: 560, y: 100, width: 120, height: 50, color: "#0077b6" },
  { id: "events", label: "Events", x: 560, y: 210, width: 120, height: 50, color: "#7c3aed" },
  { id: "store", label: "Event Store", x: 390, y: 300, width: 130, height: 50, color: "#00ADD8" },
  { id: "projections", label: "Projections", x: 190, y: 300, width: 130, height: 50, color: "#7c3aed" },
  { id: "sagas", label: "Sagas", x: 590, y: 300, width: 100, height: 50, color: "#a78bfa" },
  { id: "readmodels", label: "Read Models", x: 80, y: 390, width: 130, height: 50, color: "#0077b6" },
  { id: "outbox", label: "Outbox", x: 590, y: 390, width: 100, height: 50, color: "#00ADD8" },
];

const connections: DiagramConnection[] = [
  { from: "commands", to: "bus" },
  { from: "bus", to: "handler", label: "middleware" },
  { from: "handler", to: "aggregate" },
  { from: "aggregate", to: "events" },
  { from: "events", to: "store" },
  { from: "store", to: "projections" },
  { from: "store", to: "sagas" },
  { from: "projections", to: "readmodels" },
  { from: "sagas", to: "outbox" },
];

function getNodeCenter(node: DiagramNode) {
  return { x: node.x + node.width / 2, y: node.y + node.height / 2 };
}

function getConnectionPath(from: DiagramNode, to: DiagramNode) {
  const fc = getNodeCenter(from);
  const tc = getNodeCenter(to);

  // Determine which edge to connect from/to
  const dx = tc.x - fc.x;
  const dy = tc.y - fc.y;

  let startX: number, startY: number, endX: number, endY: number;

  if (Math.abs(dx) > Math.abs(dy)) {
    // Horizontal connection
    startX = dx > 0 ? from.x + from.width : from.x;
    startY = fc.y;
    endX = dx > 0 ? to.x : to.x + to.width;
    endY = tc.y;
  } else {
    // Vertical connection
    startX = fc.x;
    startY = dy > 0 ? from.y + from.height : from.y;
    endX = tc.x;
    endY = dy > 0 ? to.y : to.y + to.height;
  }

  const midX = (startX + endX) / 2;
  const midY = (startY + endY) / 2;

  if (Math.abs(dx) > Math.abs(dy)) {
    return `M ${startX} ${startY} C ${midX} ${startY}, ${midX} ${endY}, ${endX} ${endY}`;
  } else {
    return `M ${startX} ${startY} C ${startX} ${midY}, ${endX} ${midY}, ${endX} ${endY}`;
  }
}

export default function ArchitectureDiagram() {
  const shouldReduceMotion = useReducedMotion();
  const nodesMap = Object.fromEntries(nodes.map((n) => [n.id, n]));

  return (
    <section className="relative py-24 px-4 sm:px-6 lg:px-8">
      <div className="max-w-5xl mx-auto">
        <SectionReveal>
          <div className="text-center mb-12">
            <h2 className="text-3xl sm:text-4xl md:text-5xl font-extrabold text-white mb-4">
              <GradientText>Architecture</GradientText> that scales
            </h2>
            <p className="text-lg text-[#94a3b8] max-w-xl !mx-auto">
              A clean, layered architecture from commands to read models.
            </p>
          </div>
        </SectionReveal>

        <SectionReveal delay={0.1}>
          <div className="overflow-x-auto pb-4">
            <svg
              viewBox="0 0 780 470"
              className="w-full min-w-[600px] h-auto"
              fill="none"
              xmlns="http://www.w3.org/2000/svg"
            >
              <defs>
                <marker
                  id="arrowhead"
                  markerWidth="8"
                  markerHeight="6"
                  refX="8"
                  refY="3"
                  orient="auto"
                >
                  <polygon
                    points="0 0, 8 3, 0 6"
                    fill="rgba(148, 163, 184, 0.5)"
                  />
                </marker>
              </defs>

              {/* Connection lines */}
              {connections.map((conn, i) => {
                const fromNode = nodesMap[conn.from];
                const toNode = nodesMap[conn.to];
                const path = getConnectionPath(fromNode, toNode);

                return shouldReduceMotion ? (
                  <path
                    key={i}
                    d={path}
                    stroke="rgba(148, 163, 184, 0.3)"
                    strokeWidth="2"
                    fill="none"
                    markerEnd="url(#arrowhead)"
                  />
                ) : (
                  <motion.path
                    key={i}
                    d={path}
                    stroke="rgba(148, 163, 184, 0.3)"
                    strokeWidth="2"
                    fill="none"
                    markerEnd="url(#arrowhead)"
                    strokeDasharray="6 4"
                    initial={{ pathLength: 0, opacity: 0 }}
                    whileInView={{ pathLength: 1, opacity: 1 }}
                    viewport={{ once: true }}
                    transition={{
                      duration: 0.8,
                      delay: 0.3 + i * 0.1,
                      ease: "easeOut",
                    }}
                  />
                );
              })}

              {/* Nodes */}
              {nodes.map((node, i) => (
                <motion.g
                  key={node.id}
                  initial={
                    shouldReduceMotion ? {} : { opacity: 0, scale: 0.8 }
                  }
                  whileInView={{ opacity: 1, scale: 1 }}
                  viewport={{ once: true }}
                  transition={{
                    duration: 0.4,
                    delay: shouldReduceMotion ? 0 : i * 0.08,
                    ease: "easeOut",
                  }}
                  className="cursor-pointer"
                >
                  {/* Glow on hover */}
                  <rect
                    x={node.x - 2}
                    y={node.y - 2}
                    width={node.width + 4}
                    height={node.height + 4}
                    rx="14"
                    fill="transparent"
                    stroke="transparent"
                    strokeWidth="2"
                    className="transition-all duration-200"
                  />
                  {/* Background */}
                  <rect
                    x={node.x}
                    y={node.y}
                    width={node.width}
                    height={node.height}
                    rx="12"
                    fill="rgba(18, 18, 26, 0.9)"
                    stroke={node.color}
                    strokeWidth="1"
                    strokeOpacity="0.3"
                  />
                  {/* Label */}
                  <text
                    x={node.x + node.width / 2}
                    y={node.y + node.height / 2 + 5}
                    textAnchor="middle"
                    fill="#e2e8f0"
                    fontSize="13"
                    fontFamily="Inter, sans-serif"
                    fontWeight="600"
                  >
                    {node.label}
                  </text>
                </motion.g>
              ))}
            </svg>
          </div>
        </SectionReveal>
      </div>
    </section>
  );
}
