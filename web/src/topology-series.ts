import type { Frame, Row } from "./topology";
export type TrafficPoint = {
  at: number;
  up: number | null;
  down: number | null;
  rtt: number | null;
};
const valid = (n: number) => Number.isFinite(n) && n >= 0;

// Bounded, in-memory observations only. A reset/gap is never drawn as zero traffic.
export function routeHistories(frames: Frame[], windowSec: number) {
  const routes = new Map<
    string,
    { at: number; generation: number; up: number; down: number; rtt: number }[]
  >();
  for (const f of frames.slice(-65)) {
    const at = Date.parse(f.at);
    if (!Number.isFinite(at)) continue;
    for (const r of f.routes) {
      const list = routes.get(r.engine) || [];
      if (list.length && at <= list[list.length - 1].at) continue;
      list.push({
        at,
        generation: f.generation,
        up: r.up,
        down: r.down,
        rtt: r.rtt_us,
      });
      routes.set(r.engine, list);
    }
  }
  const result = new Map<string, TrafficPoint[]>();
  for (const [engine, points] of routes) {
    result.set(
      engine,
      points
        .map((p, i) => {
          let base = i;
          while (base > 0 && p.at - points[base].at < windowSec * 1000) base--;
          const old = points[base];
          const elapsed = (p.at - old.at) / 1000;
          const continuous =
            elapsed > 0 &&
            points
              .slice(base, i + 1)
              .every(
                (v, j, a) =>
                  v.generation === p.generation &&
                  (!j || v.at - a[j - 1].at <= 15000),
              );
          const rate = (key: "up" | "down") =>
            continuous &&
            valid(p[key]) &&
            valid(old[key]) &&
            points
              .slice(base + 1, i + 1)
              .every((v, j) => v[key] >= points[base + j][key])
              ? ((p[key] - old[key]) * 8) / elapsed / 1e6
              : null;
          return {
            at: p.at,
            up: rate("up"),
            down: rate("down"),
            rtt: Number.isFinite(p.rtt) && p.rtt > 0 ? p.rtt / 1000 : null,
          };
        })
        .filter((p) => p.at >= points[points.length - 1].at - 60000),
    );
  }
  return result;
}

export function aggregateHistory(members: Row[] = []): TrafficPoint[] {
  const unique = [
    ...new Map(
      members.map((r) => [r.worker + ":" + r.epoch + ":" + r.engine, r]),
    ).values(),
  ];
  if (!unique.length) return [];
  const indexed = unique.map(
    (r) => new Map((r.history || []).map((p) => [p.at, p])),
  );
  const times = [...new Set(indexed.flatMap((m) => [...m.keys()]))]
    .sort((a, b) => a - b)
    .slice(-65);
  const end = times[times.length - 1];
  return times
    .filter((at) => at >= end - 60000)
    .map((at) => {
      const values = indexed.map(
        (m) =>
          m.get(at) ||
          [...m.values()]
            .reverse()
            .find((p) => p.at <= at && at - p.at <= 2000),
      );
      const sum = (key: "up" | "down") =>
        values.every((p) => p && p[key] !== null)
          ? values.reduce((v, p) => v + p![key]!, 0)
          : null;
      const rtts = values
        .flatMap((p) => (p?.rtt == null ? [] : [p.rtt]))
        .sort((a, b) => a - b);
      const mid = Math.floor(rtts.length / 2);
      const rtt = !rtts.length
        ? null
        : rtts.length % 2
          ? rtts[mid]
          : (rtts[mid - 1] + rtts[mid]) / 2;
      return { at, up: sum("up"), down: sum("down"), rtt };
    });
}

export function sparkPath(
  points: TrafficPoint[],
  key: "up" | "down" | "rtt",
  maximum: number,
  end: number,
) {
  let path = "",
    previous = 0,
    connected = false;
  for (const p of points) {
    const value = p[key];
    if (value === null || !valid(value)) {
      connected = false;
      continue;
    }
    const x =
      2 + Math.max(0, Math.min(1, (p.at - (end - 60000)) / 60000)) * 252;
    const y = 42 - Math.min(1, value / Math.max(maximum, 0.001)) * 38;
    path += `${connected && p.at - previous <= 15000 ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)} `;
    previous = p.at;
    connected = true;
  }
  return path.trim();
}
