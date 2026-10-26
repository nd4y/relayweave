import { useLayoutEffect, useRef, useState } from "react";
import { OverviewNetworkArt } from "./overview-network-art";
import "./shared-network-art.css";

type Destination = "login" | "overview" | null;

/** One persistent artwork: moving its wrapper never restarts its CSS timeline. */
export function SharedNetworkArt({
  destination,
}: {
  destination: Destination;
}) {
  const layer = useRef<HTMLDivElement>(null);
  const flight = useRef<Animation | null>(null);
  const previous = useRef<Destination>(null);
  const [visible, setVisible] = useState(false);

  useLayoutEffect(() => {
    const element = layer.current!;
    const transfer = previous.current === "login" && destination === "overview";
    previous.current = destination;
    if (!destination) {
      flight.current?.cancel();
      element.dataset.moving = "false";
      setVisible(false);
      return;
    }

    const motion = matchMedia("(prefers-reduced-motion: reduce)");
    let anchor: HTMLElement | null = null;
    let first = true;
    const place = () => {
      if (!anchor) return;
      const rect = anchor.getBoundingClientRect();
      const from = element.getBoundingClientRect();
      const moving = flight.current?.playState === "running";
      const shouldFly = (first && transfer) || moving;
      first = false;
      // The anchor reserves the final layout; its arrival may follow a data wait.
      const to = `translate(${rect.left}px, ${rect.top}px) scale(${rect.width / 320})`;
      const bounds =
        destination === "overview"
          ? anchor.closest(".hero-panel")?.getBoundingClientRect()
          : null;
      const scale = rect.width / 320;
      const clip =
        bounds && scale > 0
          ? `inset(${Math.max(0, bounds.top - rect.top) / scale}px ${Math.max(0, rect.right - bounds.right) / scale}px ${Math.max(0, rect.bottom - bounds.bottom) / scale}px ${Math.max(0, bounds.left - rect.left) / scale}px)`
          : "none";
      flight.current?.cancel();
      element.style.transform = to;
      element.dataset.moving = "false";
      element.style.clipPath = clip;
      setVisible(rect.width > 0 && rect.height > 0);
      if (
        shouldFly &&
        from.width > 0 &&
        !motion.matches &&
        !document.hidden &&
        rect.width > 0
      ) {
        const animation = element.animate(
          [
            {
              transform: `translate(${from.left}px, ${from.top}px) scale(${from.width / 320})`,
            },
            { transform: to },
          ],
          { duration: 720, easing: "cubic-bezier(.22,.8,.2,1)" },
        );
        flight.current = animation;
        element.dataset.moving = "true";
        element.style.clipPath = "none";
        animation.onfinish = () => {
          element.dataset.moving = "false";
          element.style.clipPath = clip;
          if (flight.current === animation) flight.current = null;
        };
      }
    };
    const reduced = () => {
      flight.current?.cancel();
      flight.current = null;
      place();
    };
    const resize = new ResizeObserver(place);
    const find = () => {
      anchor = document.querySelector(
        `[data-network-art-anchor="${destination}"]`,
      );
      if (!anchor) return;
      waiting.disconnect();
      place();
      resize.observe(anchor);
      if (anchor.parentElement) resize.observe(anchor.parentElement);
      resize.observe(document.body);
    };
    const waiting = new MutationObserver(find);
    waiting.observe(document.body, { childList: true, subtree: true });
    find();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    motion.addEventListener("change", reduced);
    document.addEventListener("visibilitychange", reduced);
    return () => {
      waiting.disconnect();
      resize.disconnect();
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
      motion.removeEventListener("change", reduced);
      document.removeEventListener("visibilitychange", reduced);
    };
  }, [destination]);

  useLayoutEffect(
    () => () => {
      flight.current?.cancel();
    },
    [],
  );

  return (
    <div className="rw-auth-art-viewport" aria-hidden="true">
      <div
        ref={layer}
        className="rw-auth-art-layer"
        hidden={!visible}
        aria-hidden="true"
        data-destination={destination ?? "none"}
      >
        <OverviewNetworkArt shared />
      </div>
    </div>
  );
}
