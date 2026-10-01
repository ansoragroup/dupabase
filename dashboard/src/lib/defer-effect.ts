// Start external work after the effect has returned. Cleanup also prevents a
// Strict Mode replay or an unmount from starting obsolete work.
export function deferEffect(work: () => void | (() => void)) {
  let cancelled = false;
  let cleanup: void | (() => void);
  queueMicrotask(() => {
    if (!cancelled) cleanup = work();
  });
  return () => {
    cancelled = true;
    cleanup?.();
  };
}
