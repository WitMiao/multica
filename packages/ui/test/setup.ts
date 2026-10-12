import "@testing-library/jest-dom/vitest";

// jsdom ships neither class. use-scroll-fade observes both.
class ResizeObserverStub implements ResizeObserver {
  observe(): void {}

  unobserve(): void {}

  disconnect(): void {}
}

if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = ResizeObserverStub;
}

// jsdom never runs its own frame loop. Queue callbacks so tests can flush
// exactly one frame at a time through `flushRaf()`.
const rafQueue: FrameRequestCallback[] = [];
globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
  rafQueue.push(cb);
  return rafQueue.length;
};
globalThis.cancelAnimationFrame = (): void => {};
globalThis.flushRaf = (): void => {
  const pending = rafQueue.splice(0, rafQueue.length);
  for (const cb of pending) cb(performance.now());
};

declare global {
  var flushRaf: () => void;
}
