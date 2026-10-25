import React, { useEffect, useState } from "react";
import {
  ArrowRight,
  Check,
  ChevronRight,
  Pause,
  Play,
  RotateCcw,
} from "lucide-react";
import { demoEvents, projectOrder } from "./replay";

export default function EventReplay() {
  const [position, setPosition] = useState(4);
  const [playing, setPlaying] = useState(false);
  const order = projectOrder(position);
  useEffect(() => {
    if (!playing) return;
    const timer = window.setInterval(
      () =>
        setPosition((previous) => Math.min(previous + 1, demoEvents.length)),
      950,
    );
    return () => window.clearInterval(timer);
  }, [playing]);
  useEffect(() => {
    if (position === demoEvents.length) setPlaying(false);
  }, [position]);
  const seek = (next: number) => {
    setPlaying(false);
    setPosition(next);
  };
  const replay = () => {
    if (playing) {
      setPlaying(false);
      return;
    }
    if (position === demoEvents.length) setPosition(0);
    setPlaying(true);
  };
  return (
    <div
      className="mk-replay"
      role="region"
      aria-label="Interactive event sourcing demo"
    >
      <div className="mk-windowbar">
        <span>
          <i /> event explorer
        </span>
        <span className="mk-demo-label">INTERACTIVE DEMO</span>
      </div>
      <div className="mk-stream-heading">
        <span className="mk-mono">
          orders / <b>order-042</b>
        </span>
        <span className="mk-pill">4 events</span>
      </div>
      <div className="mk-replay-flow">
        <div
          className="mk-event-list"
          role="group"
          aria-label="Choose an event to replay through"
        >
          {demoEvents.map((event, index) => (
            <button
              key={event.type}
              type="button"
              className={`mk-event mk-event--${event.color} ${position > index ? "is-applied" : ""} ${position === index + 1 ? "is-current" : ""}`}
              onClick={() => seek(index + 1)}
              aria-pressed={position === index + 1}
              aria-label={`Replay through event ${index + 1}: ${event.type}`}
            >
              <span className="mk-event-node">
                {position > index ? <Check size={12} /> : index + 1}
              </span>
              <span className="mk-event-content">
                <span className="mk-event-type">{event.type}</span>
                <span className="mk-event-detail">{event.detail}</span>
              </span>
              <span className="mk-event-version">v{index + 1}</span>
            </button>
          ))}
        </div>
        <div className="mk-replay-bridge" aria-hidden="true">
          <span />
          <ArrowRight size={20} />
          <small>project</small>
        </div>
        <div className="mk-read-model">
          <div className="mk-model-label">
            <span className="mk-model-icon">≡</span> READ MODEL
          </div>
          <div className="mk-order-title">
            Order summary<small>#042</small>
          </div>
          <dl>
            <div>
              <dt>Customer</dt>
              <dd>{order.customer}</dd>
            </div>
            <div>
              <dt>Items</dt>
              <dd>{order.items}</dd>
            </div>
            <div className="mk-total">
              <dt>Total</dt>
              <dd>{order.total}</dd>
            </div>
          </dl>
          <span
            className={`mk-order-status ${position === 4 ? "is-shipped" : ""}`}
          >
            <span />
            {order.status}
          </span>
          <div className="mk-model-version">
            rebuilt from <strong>{position}</strong> / 4 events
          </div>
        </div>
      </div>
      <div className="mk-replay-controls">
        <button type="button" className="mk-replay-play" onClick={replay}>
          {playing ? <Pause size={14} /> : <Play size={14} />}{" "}
          {playing ? "Pause" : position === 4 ? "Replay" : "Play"}
        </button>
        <label className="mk-scrubber">
          <span className="sr-only">Replay position</span>
          <input
            type="range"
            min="0"
            max="4"
            value={position}
            onChange={(event) => seek(Number(event.target.value))}
            aria-valuetext={`${position} of 4 events, order ${order.status.toLowerCase()}`}
          />
        </label>
        <button
          className="mk-icon-button"
          type="button"
          onClick={() => seek(Math.min(position + 1, 4))}
          disabled={position === 4}
          aria-label="Apply next event"
        >
          <ChevronRight size={17} />
        </button>
        <button
          className="mk-icon-button"
          type="button"
          onClick={() => seek(0)}
          aria-label="Reset replay"
        >
          <RotateCcw size={14} />
        </button>
      </div>
      <div className="mk-replay-caption">
        <span className="mk-status-dot" /> Same events. Same state. Every
        replay.
        <span className="sr-only" role="status">
          {position} events applied. Status: {order.status}. Total:{" "}
          {order.total}.
        </span>
      </div>
    </div>
  );
}
