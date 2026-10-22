import { test, expect, type Page } from "@playwright/test";

const server = Date.parse("2026-10-22T12:00:00Z");
const iso = (ms: number) => new Date(ms).toISOString();
function frame(sequence = 1, at = iso(server - 800)) {
  return {
    worker: "worker-1", epoch: "worker-epoch", sequence, at, generation: 1,
    catalog_size: 0, routes: [], listeners: [], decisions: [], changes: [],
    sampled: 0, sample_every: 1, dropped: 0, udp_active: 0, lease_until: 0,
    truncated: false, prepared_at: at,
  };
}
function snapshot(workers = [frame()], serverTime = server, cursor = "cp:1") {
  return { schema: 1, server_time: iso(serverTime), cursor, workers,
    completeness: "partial", limitations: [], retention: {} };
}

async function mock(page: Page, skew = 5000, initial = snapshot()) {
  let current = initial;
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.clock.install({ time: new Date(server + skew) });
  await page.addInitScript(() => {
    const sources: any[] = [];
    (window as any).__freshnessSources = sources;
    class FakeSource extends EventTarget {
      url: string;
      readyState = 1;
      onerror: ((event: Event) => void) | null = null;
      constructor(url: string) { super(); this.url = String(url); sources.push(this); }
      close() { this.readyState = 2; }
    }
    (window as any).EventSource = FakeSource;
  });
  await page.route("**/api/v1/**", (route) => {
    const path = new URL(route.request().url()).pathname;
    return route.fulfill({ json: path.endsWith("/topology/snapshot") ? current :
      path.endsWith("/auth/session") ? { csrf: "synthetic" } : { items: [] } });
  });
  await page.goto("/#topology");
  const badge = page.getByTestId("topology-freshness");
  await expect(badge).toBeVisible();
  return { badge, errors, setSnapshot: (value: typeof initial) => { current = value; } };
}

async function emit(page: Page, type: string, data: unknown, index = -1) {
  await page.evaluate(({ type, data, index }) => {
    const sources = (window as any).__freshnessSources.filter((s: any) => s.url.includes("/topology/events?"));
    const source = index < 0 ? sources[sources.length - 1] : sources[index];
    source.dispatchEvent(new MessageEvent(type, { data: typeof data === "string" ? data : JSON.stringify(data) }));
  }, { type, data, index });
}

for (const skew of [5000, 3600000]) {
  test(`freshness does not flap with client clock +${skew}ms; stopped worker stays stale through heartbeat/reconnect`, async ({ page }) => {
    const h = await mock(page, skew);
    await expect(h.badge).toHaveText("Live");
    await h.badge.evaluate((node) => {
      (window as any).__freshnessLabels = [];
      new MutationObserver(() => (window as any).__freshnessLabels.push(node.textContent))
        .observe(node, { subtree: true, childList: true, characterData: true });
    });
    for (let second = 1; second <= 12; second++) {
      await page.clock.runFor(1000);
      await emit(page, "frame", { cursor: `cp:${second + 1}`, worker: "worker-1",
        frame: frame(second + 1, iso(server + second * 1000 - 800)) });
      if (second % 10 === 0) await emit(page, "heartbeat", { server_time: iso(server + second * 1000) });
      await expect(h.badge).toHaveText("Live");
    }
    const labels = await page.evaluate(() => (window as any).__freshnessLabels as string[]);
    expect(labels.some((x) => x.includes("Устаревшие") || x.includes("неизвестна"))).toBe(false);
    // Keep the CP heartbeat alive while the worker's sequence/timestamp stop.
    await page.clock.runFor(6000);
    await expect(h.badge).toHaveText("Устаревшие данные");
    await emit(page, "heartbeat", { server_time: iso(server + 18000) });
    await emit(page, "heartbeat", "{malformed");
    await expect(h.badge).toHaveText("Устаревшие данные");

    h.setSnapshot(snapshot([frame(13, iso(server + 11200))], server + 18000, "cp:13"));
    await page.evaluate(() => {
      const s = (window as any).__freshnessSources.filter((x: any) => x.url.includes("/topology/events?"));
      s[s.length - 1].onerror?.(new Event("error"));
    });
    await page.clock.runFor(1100);
    await expect.poll(() => page.evaluate(() => (window as any).__freshnessSources
      .filter((x: any) => x.url.includes("/topology/events?")).length)).toBe(2);
    await expect(h.badge).toHaveText("Устаревшие данные");
    // A queued callback from the closed stream must not restore Live or its cursor.
    const fresh = { cursor: "cp:14", worker: "worker-1", frame: frame(14, iso(server + 18300)) };
    await emit(page, "frame", fresh, 0);
    await expect(h.badge).toHaveText("Устаревшие данные");
    await emit(page, "frame", fresh);
    await expect(h.badge).toHaveText("Live");
    expect(h.errors).toEqual([]);
  });
}

for (const missing of [true, false]) {
  test(`missing/invalid worker telemetry never claims Live (${missing ? "missing" : "invalid"})`, async ({ page }) => {
    const h = await mock(page, 3600000, snapshot(missing ? [] : [frame(1, "not-a-time")]));
    await expect(h.badge).toHaveText("Свежесть неизвестна");
    await emit(page, "heartbeat", { server_time: iso(server + 1000) });
    await expect(h.badge).toHaveText("Свежесть неизвестна");
    await emit(page, "frame", { cursor: "cp:2", worker: "worker-1", frame: frame(2, iso(server + 1000)) });
    await expect(h.badge).toHaveText("Live");
    expect(h.errors).toEqual([]);
  });
}
