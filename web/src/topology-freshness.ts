// Keep the existing worker stale deadline. Worker telemetry is sampled every
// second; the independent SSE heartbeat is every ten seconds, not a new sample.
export const WORKER_STALE_MS = 5000;
const FUTURE_TOLERANCE_MS = 1000;

type WorkerStamp = {
  worker: string;
  epoch: string;
  sequence: number;
  at: string;
};
type Observation = {
  epoch: string;
  sequence: number;
  at: string;
  expires: number | null;
};
export type Freshness = "fresh" | "stale" | "unknown";

function timestamp(value: string): number | null {
  const parsed = typeof value === "string" ? Date.parse(value) : NaN;
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
}

export class TopologyFreshness {
  private clock: { server: number; received: number; uncertainty: number } | null = null;
  private workers = new Map<string, Observation>();

  snapshot(snapshot: { server_time: string; workers: WorkerStamp[] }, now: number, requestMs = 0) {
    const server = timestamp(snapshot.server_time);
    this.clock = server === null ? null : {
      server, received: now, uncertainty: Math.max(0, requestMs),
    };
    const current = new Set(snapshot.workers.map((worker) => worker.worker));
    for (const id of this.workers.keys()) {
      if (!current.has(id)) this.workers.delete(id);
    }
    for (const worker of snapshot.workers) this.frame(worker, now);
  }

  heartbeat(serverTime: string, now: number) {
    const server = timestamp(serverTime);
    if (server !== null) {
      this.clock = { server, received: now, uncertainty: this.clock?.uncertainty ?? 0 };
    }
    // Never renew a worker's deadline: a live CP can retain a stopped worker.
  }

  frame(worker: WorkerStamp, now: number) {
    const previous = this.workers.get(worker.worker);
    const at = timestamp(worker.at);
    const valid = Boolean(worker.worker && worker.epoch) &&
      Number.isSafeInteger(worker.sequence) && worker.sequence > 0 && at !== null;
    if (valid && previous && previous.epoch === worker.epoch && previous.sequence === worker.sequence && previous.at === worker.at) {
      return; // Reconnect snapshots/replayed frames cannot make old data fresh.
    }
    let expires: number | null = null;
    if (valid && this.clock && now >= this.clock.received &&
        !(previous && previous.epoch === worker.epoch && worker.sequence <= previous.sequence)) {
      // server_time is CP wall time; all elapsed time is monotonic browser time.
      // Browser Date.now(), its offset and later wall-clock jumps are irrelevant.
      const serverNow = this.clock.server + now - this.clock.received;
      if (at! <= serverNow + this.clock.uncertainty + FUTURE_TOLERANCE_MS) {
        expires = now + WORKER_STALE_MS - Math.max(0, serverNow - at!);
      }
    }
    this.workers.set(worker.worker, {
      epoch: worker.epoch, sequence: worker.sequence, at: worker.at, expires,
    });
    while (this.workers.size > 32) this.workers.delete(this.workers.keys().next().value!);
  }

  state(workers: WorkerStamp[], now: number): Freshness {
    if (!workers.length || !this.clock || !Number.isFinite(now) || now < this.clock.received) return "unknown";
    let unknown = false;
    for (const worker of workers) {
      const sample = this.workers.get(worker.worker);
      if (!sample || sample.expires === null) unknown = true;
      else if (now > sample.expires) return "stale";
    }
    return unknown ? "unknown" : "fresh";
  }
}
