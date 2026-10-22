import assert from "node:assert/strict";
import test from "node:test";
import { TopologyFreshness, WORKER_STALE_MS } from "../src/topology-freshness.ts";

const server = Date.parse("2026-10-22T12:00:00Z");
const iso = (ms: number) => new Date(ms).toISOString();
const frame = (sequence = 1, at = server - 800, epoch = "worker-epoch") => ({
  worker: "worker-1", epoch, sequence, at: iso(at),
});

test("client +5s/+1h offsets and wall-clock steps never affect normal 1s telemetry", () => {
  const original = Date.now;
  try {
    for (const offset of [5000, 3600000, -3600000]) {
      Date.now = () => server + offset;
      const tracker = new TopologyFreshness();
      let worker = frame();
      tracker.snapshot({ server_time: iso(server), workers: [worker] }, 0);
      for (let second = 1; second <= 40; second++) {
        assert.equal(tracker.state([worker], second * 1000), "fresh");
        worker = frame(second + 1, server + second * 1000 - 800);
        tracker.frame(worker, second * 1000);
        if (second % 10 === 0) tracker.heartbeat(iso(server + second * 1000), second * 1000);
        assert.equal(tracker.state([worker], second * 1000 + 999), "fresh");
      }
      Date.now = () => server + offset + 86400000;
      assert.equal(tracker.state([worker], 40000 + WORKER_STALE_MS), "stale");
    }
  } finally {
    Date.now = original;
  }
});

test("the 5s worker deadline is unchanged; heartbeat is not worker progress", () => {
  const tracker = new TopologyFreshness();
  const worker = frame();
  tracker.snapshot({ server_time: iso(server), workers: [worker] }, 0);
  assert.equal(WORKER_STALE_MS, 5000);
  assert.equal(tracker.state([worker], 4200), "fresh");
  assert.equal(tracker.state([worker], 4201), "stale");
  tracker.heartbeat(iso(server + 10000), 10000);
  assert.equal(tracker.state([worker], 10000), "stale");
  tracker.heartbeat("not-a-time", 11000);
  assert.equal(tracker.state([worker], 11000), "stale");
});

test("reconnect snapshot/replayed frames cannot renew a cached worker", () => {
  const tracker = new TopologyFreshness();
  const worker = frame();
  tracker.snapshot({ server_time: iso(server), workers: [worker] }, 0);
  tracker.snapshot({ server_time: iso(server + 10000), workers: [worker] }, 10000);
  tracker.frame(worker, 10001);
  assert.equal(tracker.state([worker], 10001), "stale");
  const next = frame(2, server + 10200);
  tracker.frame(next, 11000);
  assert.equal(tracker.state([next], 11000), "fresh");
});

test("already-old snapshots stay stale even immediately after receipt", () => {
  const tracker = new TopologyFreshness();
  const worker = frame(1, server - 60000);
  tracker.snapshot({ server_time: iso(server), workers: [worker] }, 10);
  assert.equal(tracker.state([worker], 10), "stale");
});

test("empty/missing/invalid timestamps, sequence and impossible future time are unknown", () => {
  for (const worker of [
    { ...frame(), at: "bad" }, { ...frame(), at: "" },
    { ...frame(), at: "0001-01-01T00:00:00Z" },
    { ...frame(), epoch: "" }, { ...frame(), sequence: NaN }, frame(1, server + 60000),
  ]) {
    const tracker = new TopologyFreshness();
    tracker.snapshot({ server_time: iso(server), workers: [worker] }, 0);
    assert.equal(tracker.state([worker], 0), "unknown");
  }
  const tracker = new TopologyFreshness();
  tracker.snapshot({ server_time: "bad", workers: [frame()] }, 0);
  assert.equal(tracker.state([frame()], 0), "unknown");
  assert.equal(tracker.state([], 0), "unknown");
});

test("one stopped worker remains stale while another worker advances", () => {
  const tracker = new TopologyFreshness();
  const stopped = frame();
  let active = { ...frame(), worker: "worker-2" };
  tracker.snapshot({ server_time: iso(server), workers: [stopped, active] }, 0);
  for (let i = 1; i <= 10; i++) {
    active = { ...frame(i + 1, server + i * 1000 - 800), worker: "worker-2" };
    tracker.frame(active, i * 1000);
  }
  assert.equal(tracker.state([stopped, active], 10000), "stale");
});

test("worker restart epoch recovers, but regressing sequence does not become fresh", () => {
  const tracker = new TopologyFreshness();
  tracker.snapshot({ server_time: iso(server), workers: [frame(10)] }, 0);
  tracker.frame(frame(9, server), 1);
  assert.equal(tracker.state([frame(9)], 1), "unknown");
  const restarted = frame(1, server + 1000, "new-epoch");
  tracker.frame(restarted, 1000);
  assert.equal(tracker.state([restarted], 1000), "fresh");
});
