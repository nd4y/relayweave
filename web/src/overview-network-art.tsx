import { useEffect, useRef, useState } from "react";
import AltRouteRounded from "@mui/icons-material/AltRouteRounded";
import BoltRounded from "@mui/icons-material/BoltRounded";
import HubRounded from "@mui/icons-material/HubRounded";
import ShieldRounded from "@mui/icons-material/ShieldRounded";
import "./overview-network-art.css";

// Decorative choreography only. It never represents live traffic or capacity.
export function OverviewNetworkArt({ shared = false }: { shared?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const [paused, setPaused] = useState(true);
  const [reduced, setReduced] = useState(true);

  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
    let visible = false;
    const sync = () => {
      setReduced(motion.matches);
      setPaused(motion.matches || document.hidden || !visible);
    };
    const observer = new IntersectionObserver(([entry]) => {
      visible = entry.isIntersecting;
      sync();
    });
    const hide = () => {
      visible = false;
      setPaused(true);
      observer.disconnect();
    };
    const show = () => {
      observer.observe(element);
      sync();
    };
    motion.addEventListener("change", sync);
    document.addEventListener("visibilitychange", sync);
    window.addEventListener("pagehide", hide);
    window.addEventListener("pageshow", show);
    observer.observe(element);
    sync();
    return () => {
      observer.disconnect();
      motion.removeEventListener("change", sync);
      document.removeEventListener("visibilitychange", sync);
      window.removeEventListener("pagehide", hide);
      window.removeEventListener("pageshow", show);
    };
  }, []);

  return (
    <div
      ref={ref}
      className="network-art rw-ov-art"
      aria-hidden="true"
      data-paused={paused}
      data-static={reduced}
      data-shared={shared}
    >
      <div className="art-ring ring-a" />
      <div className="art-ring ring-b" />
      <svg className="rw-ov-scale-map" viewBox="0 0 320 260">
        <path
          className="rw-ov-entry-line"
          pathLength="100"
          d="M 12 131 H 126"
        />
        <path
          className="rw-ov-fan-out"
          pathLength="100"
          d="M 143 105 Q 111 75 67 65 M 199 120 L 280 101 M 163 167 V 236"
        />
        <path
          className="rw-ov-expansion"
          pathLength="100"
          d="M 67 65 Q 52 39 23 24 M 67 65 Q 81 24 112 10 M 280 101 Q 304 74 309 34 M 280 101 Q 310 126 309 176 M 162 236 Q 205 230 234 251 M 162 236 Q 107 220 57 240"
        />
        <g className="rw-ov-far-points">
          <circle cx="23" cy="24" r="2.5" />
          <circle cx="112" cy="10" r="2" />
          <circle cx="309" cy="34" r="2.5" />
          <circle cx="309" cy="176" r="2" />
          <circle cx="234" cy="251" r="2.5" />
          <circle cx="57" cy="240" r="2" />
        </g>
      </svg>
      <svg className="rw-ov-signal-arc" viewBox="0 0 320 260">
        <path
          pathLength="100"
          d="M 283 44 A 150 70 -35 1 1 37 216 A 150 70 -35 1 1 283 44"
        />
      </svg>
      <div className="rw-ov-traveler">
        <i className="rw-ov-packet rw-ov-tail-three" />
        <i className="rw-ov-packet rw-ov-tail-two" />
        <i className="rw-ov-packet rw-ov-tail-one" />
        <i className="rw-ov-packet" />
      </div>
      <div className="art-center rw-ov-center">
        <i className="rw-ov-halo" />
        <HubRounded sx={{ fontSize: 48 }} />
      </div>
      <div className="art-node node-a rw-ov-node rw-ov-node-a">
        <BoltRounded />
      </div>
      <div className="art-node node-b rw-ov-node rw-ov-node-b">
        <ShieldRounded />
      </div>
      <div className="art-node node-c rw-ov-node rw-ov-node-c">
        <AltRouteRounded />
      </div>
    </div>
  );
}
