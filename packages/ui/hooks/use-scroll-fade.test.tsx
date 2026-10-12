// @vitest-environment jsdom
import { render, act } from "@testing-library/react";
import { type CSSProperties, useRef, useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { useScrollFade } from "./use-scroll-fade";

/** jsdom reports every box as 0×0; these getters stand in for real layout. */
function fakeScrollBox(
  el: HTMLElement,
  box: { client: number; scroll: number; position?: number },
): void {
  Object.defineProperty(el, "clientHeight", { configurable: true, value: box.client });
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: box.scroll });
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    value: box.position ?? 0,
  });
}

// jsdom's cssstyle drops `mask-image`, so assert on the hook's return value.
function Host({
  box,
  onRender,
  onStyle,
}: {
  box: { client: number; scroll: number; position?: number };
  onRender?: () => void;
  onStyle?: (style: CSSProperties | undefined) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [, bump] = useState(0);
  const style = useScrollFade(ref);
  onRender?.();
  onStyle?.(style);
  return (
    <div
      ref={(node) => {
        ref.current = node;
        if (node) fakeScrollBox(node, box);
      }}
    >
      <button type="button" onClick={() => bump((n) => n + 1)}>
        bump
      </button>
    </div>
  );
}

describe("useScrollFade", () => {
  it("returns no mask for a non-scrollable box", () => {
    const styles: (CSSProperties | undefined)[] = [];
    render(<Host box={{ client: 100, scroll: 100 }} onStyle={(s) => styles.push(s)} />);
    act(() => flushRaf());
    expect(styles.at(-1)).toBeUndefined();
  });

  it("returns the end fade at the top of a scrollable box", () => {
    const styles: (CSSProperties | undefined)[] = [];
    render(<Host box={{ client: 100, scroll: 400 }} onStyle={(s) => styles.push(s)} />);
    act(() => flushRaf());
    expect(String(styles.at(-1)?.maskImage)).toContain("transparent 100%");
  });

  it("keeps the fade value when the box overflows and position does not change", () => {
    // Mount and resize storms used to dispatch an identical setState per
    // observer hit — hundreds per second across the shell's fade hosts, every
    // one of them scheduled through React. Identical values must not reach
    // React at all.
    const renders = vi.fn();
    const styles: (CSSProperties | undefined)[] = [];
    const { container } = render(
      <Host box={{ client: 100, scroll: 400 }} onRender={renders} onStyle={(s) => styles.push(s)} />,
    );
    act(() => flushRaf());
    const afterMount = renders.mock.calls.length;
    const box = container.querySelector("div")!;
    // Simulate the storm: far more mutations and scroll events than frames,
    // with layout unchanged throughout.
    act(() => {
      for (let i = 0; i < 50; i++) {
        box.dispatchEvent(new Event("scroll"));
        box.appendChild(document.createElement("span"));
        box.removeChild(box.lastChild!);
      }
      flushRaf();
    });
    expect(renders.mock.calls.length).toBe(afterMount);
    expect(String(styles.at(-1)?.maskImage)).toContain("transparent 100%");
  });

  it("updates the fade when the scroll position crosses a boundary", () => {
    const styles: (CSSProperties | undefined)[] = [];
    const { container } = render(
      <Host box={{ client: 100, scroll: 400 }} onStyle={(s) => styles.push(s)} />,
    );
    act(() => flushRaf());
    const box = container.querySelector("div")!;
    act(() => {
      fakeScrollBox(box, { client: 100, scroll: 400, position: 150 });
      box.dispatchEvent(new Event("scroll"));
      flushRaf();
    });
    const mask = String(styles.at(-1)?.maskImage ?? "");
    expect(mask).toContain("transparent 0%");
    expect(mask).toContain("transparent 100%");
  });
});
