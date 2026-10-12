import { type RefObject, type CSSProperties, useEffect, useState } from "react";

export type ScrollFadeAxis = "vertical" | "horizontal";

/**
 * Returns a dynamic maskImage style based on scroll position.
 * - At start → fade end only
 * - At end → fade start only
 * - In middle → fade both
 * - No overflow → undefined (no mask)
 */
export function useScrollFade(
  ref: RefObject<HTMLElement | null>,
  fadeSize = 32,
  axis: ScrollFadeAxis = "vertical",
): CSSProperties | undefined {
  const [fade, setFade] = useState<"none" | "start" | "end" | "both">("none");

  useEffect(() => {
    const el = ref.current;
    if (!el) return;

    // Scroll, resize and child mutations all funnel through one rAF-coalesced
    // run. Each run reads layout once and calls setFade only when the value
    // actually changes. With dozens of fade hosts on screen — the sidebar
    // groups and the tab bar alone mount many — a mount or a realtime-event
    // re-render used to fire hundreds of identical dispatchSetState calls per
    // second, every one of them going through React's lane scheduler, and
    // that stretched tab switches and route remounts out to seconds.
    let last: "none" | "start" | "end" | "both" | null = null;
    let raf = 0;
    const run = () => {
      raf = 0;
      const node = ref.current;
      if (!node) return;
      const position = axis === "horizontal" ? node.scrollLeft : node.scrollTop;
      const scrollSize = axis === "horizontal" ? node.scrollWidth : node.scrollHeight;
      const clientSize = axis === "horizontal" ? node.clientWidth : node.clientHeight;
      const scrollable = scrollSize - clientSize;
      const atStart = position <= 1;
      const atEnd = position >= scrollable - 1;
      const next: "none" | "start" | "end" | "both" =
        scrollable <= 0 || (atStart && atEnd) ? "none" : atStart ? "end" : atEnd ? "start" : "both";
      if (next !== last) {
        last = next;
        setFade(next);
      }
    };
    const schedule = () => {
      if (!raf) raf = requestAnimationFrame(run);
    };

    schedule();
    el.addEventListener("scroll", schedule, { passive: true });
    const ro = new ResizeObserver(schedule);
    ro.observe(el);
    // ResizeObserver only fires on the container's own box. When children
    // grow inside a flex/auto-sized parent, the scroll extent can change while
    // the viewport does not — the mask would stay "none" until the user scrolls.
    // MutationObserver on childList catches those content insertions.
    const mo = new MutationObserver(schedule);
    mo.observe(el, { childList: true, subtree: true });

    return () => {
      if (raf) cancelAnimationFrame(raf);
      el.removeEventListener("scroll", schedule);
      ro.disconnect();
      mo.disconnect();
    };
  }, [axis, ref]);

  if (fade === "none") return undefined;

  const start =
    fade === "start" || fade === "both"
      ? `transparent 0%, black ${fadeSize}px`
      : "black 0%";
  const end =
    fade === "end" || fade === "both"
      ? `black calc(100% - ${fadeSize}px), transparent 100%`
      : "black 100%";

  const direction = axis === "horizontal" ? "right" : "bottom";
  const gradient = `linear-gradient(to ${direction}, ${start}, ${end})`;

  return {
    maskImage: gradient,
    WebkitMaskImage: gradient,
  };
}
