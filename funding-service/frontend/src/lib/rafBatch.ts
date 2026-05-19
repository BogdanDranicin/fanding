type UpdateFn = () => void;

let pending: UpdateFn[] = [];
let rafId: number | null = null;

export function scheduleUpdate(fn: UpdateFn): void {
  pending.push(fn);
  if (rafId === null) {
    rafId = requestAnimationFrame(() => {
      const toRun = pending;
      pending = [];
      rafId = null;
      for (const f of toRun) f();
    });
  }
}
