import { test, expect, type Page } from "@playwright/test";
import fs from "node:fs";
import {
  routeHistories,
  aggregateHistory,
  sparkPath,
} from "../src/topology-series";

function snapshot() {
  const at = new Date().toISOString();
  const route = (id: string, ips: string[], worker = "worker-test") => ({
    id,
    engine: "engine-" + id,
    core: "xray",
    version: id === "route-2" ? "25.12.8" : "26.10.17",
    subscription: "source-" + id,
    server_host: "demo-" + id + ".invalid",
    server_ips: ips,
    dns_at: at,
    group: "legacy-" + id,
    state: "ready",
    active: 2,
    connections: 8,
    failures: 0,
    up: 1000,
    down: 10000,
    speed_bps: 0,
    measurement_at: 0,
    rtt_us: 42000,
    valid_until: at,
    worker,
  });
  const decision = {
    id: "decision-test",
    worker: "worker-test",
    at,
    generation: 1,
    policy: "policy-test",
    mode: "adaptive",
    method: "p2c",
    listener: "http-test",
    protocol: "http_connect",
    selected: "route-1",
    reason: "selected_by_policy",
    candidates: [
      {
        route: "route-1",
        engine: "engine-route-1",
        group: "shared-test",
        active: 2,
        reason: "compared",
        score: 1.25,
        effective_rtt_ms: 42,
        factors: ["distinct_server_ip_preferred"],
        speed_bps: 0,
        measurement_at: 0,
      },
      {
        route: "route-3",
        engine: "engine-route-3",
        group: "shared-test",
        active: 4,
        reason: "shared_balancer_deprioritized",
        speed_bps: 0,
        measurement_at: 0,
      },
    ],
    truncated: false,
    prepared_at: at,
  };
  return {
    schema: 1,
    server_time: at,
    cursor: "test:1",
    completeness: "partial",
    limitations: [],
    retention: {},
    workers: [
      {
        worker: "worker-test",
        epoch: "test",
        sequence: 1,
        at,
        generation: 1,
        catalog_size: 8,
        routes: [
          route("route-1", ["203.0.113.10"]),
          route("route-2", ["203.0.113.10", "203.0.113.11"]),
          route("route-3", ["203.0.113.11"]),
          route("other", ["198.51.100.20"]),
          route("unknown-a", []),
          route("unknown-b", []),
          route("fifth", ["192.0.2.20"]),
        ],
        listeners: [
          { id: "http-test", protocol: "http", policy: "policy-test" },
          { id: "socks-test", protocol: "socks5", policy: "policy-test" },
        ],
        decisions: [decision],
        changes: [],
        sampled: 0,
        sample_every: 1,
        dropped: 0,
        udp_active: 0,
        lease_until: Date.now() / 1000 + 60,
        truncated: false,
        prepared_at: at,
      },
    ],
  };
}
async function fixture(page: Page, value = snapshot()) {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/topology/snapshot"))
      return route.fulfill({ json: value });
    if (path.includes("/topology/decisions/"))
      return route.fulfill({ json: value.workers[0].decisions[0] });
    if (path.includes("/topology/events") || path.includes("/events/stream"))
      return route.fulfill({
        contentType: "text/event-stream",
        body: ": keepalive\n\n",
      });
    return route.fulfill({
      json: path.endsWith("/auth/session")
        ? { csrf: "test" }
        : { items: [], total: 0, counts: {}, workers: [] },
    });
  });
  await page.goto("/#topology");
  await expect(page.getByTestId("topology-flow")).toHaveAttribute(
    "data-layout-count",
    "1",
    { timeout: 40000 },
  );
  await expect(
    page.getByText(/Автоматическая раскладка недоступна/),
  ).toHaveCount(0);
}

test("ISP paths remain visible inside shared server IP groups", async ({ page }) => {
  const value = snapshot();
  value.workers[0].routes.forEach((r, i) => Object.assign(r, { egress: i === 1 ? "isp2" : "isp1", profile_id: "profile-" + r.id }));
  await fixture(page, value);
  const flow = page.getByTestId("topology-flow");
  const shared = flow.locator(".react-flow__node").filter({ has: page.locator('[data-model-id="ip-group:203.0.113.10|203.0.113.11"]') });
  await expect(shared).toContainText("isp1 / isp2");
  await shared.getByRole("button", { name: "Раскрыть профили (3)" }).click();
  await expect(flow.locator('[data-model-id^="profile:"]').filter({ hasText: "isp2 · demo-route-2.invalid" })).toHaveCount(1);
});

test("ISP settings preserve a disabled path and submit explicit source bindings", async ({ page }) => {
  await fixture(page);
  const item = { kind: "settings", id: "global", version: 1, name: "Global", data: { default_core: "26.10.17", udp_advertise: "auto", udp_port_min: 31000, udp_port_max: 31255, offline_lease_seconds: 86400, proxy_bind: "0.0.0.0", download_mbps: 800, upload_mbps: 500, egress_paths: [ { id: "isp1", name: "Primary", source_ip: "192.0.2.11", enabled: false, weight: 8, download_mbps: 760, upload_mbps: 760 }, { id: "isp2", name: "Secondary", source_ip: "192.0.2.12", enabled: true, weight: 5, download_mbps: 475, upload_mbps: 475 } ] } };
  let submitted: any;
  await page.route("**/api/v1/settings**", async route => {
    if (route.request().method() === "PATCH") { submitted = route.request().postDataJSON(); return route.fulfill({ json: { ...item, version: 2 } }); }
    return route.fulfill({ json: { items: [item], total: 1 } });
  });
  await page.goto("/#settings");
  await expect(page.getByText("Каналы ISP", { exact: true })).toBeVisible();
  const switches = page.getByRole("switch");
  await expect(switches.nth(0)).not.toBeChecked();
  await expect(switches.nth(1)).toBeChecked();
  await page.getByRole("button", { name: "Сохранить настройки", exact: true }).click();
  await expect.poll(() => submitted?.data.egress_paths[1].source_ip).toBe("192.0.2.12");
  expect(submitted.data.egress_paths[0].enabled).toBe(false);
});

test("IP overlap groups, expansion, decision evidence, pagination and unknown addresses", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const writes: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes("/api/") && r.method() !== "GET")
      writes.push(r.method());
  });
  await fixture(page);
  expect(
    (await page.request.get("/")).headers()["content-security-policy"],
  ).toContain("worker-src 'self'");
  const flow = page.getByTestId("topology-flow");
  await page.getByRole("button", { name: "Вместить", exact: true }).click();
  await expect(page.getByText("IP-группы 1-4 из 5")).toBeVisible();
  const shared = flow.locator(".react-flow__node").filter({
    has: page.locator('[data-model-id="ip-group:203.0.113.10|203.0.113.11"]'),
  });
  await expect(shared).toHaveCount(1);
  await shared.getByRole("button", { name: "Раскрыть профили (3)" }).click();
  await expect(flow.locator('[data-model-id^="profile:"]')).toHaveCount(3);
  await expect(flow).toHaveAttribute("data-layout-count", "2");
  await shared.locator(".rw-card-title").first().click();
  await expect(page.getByText("Последние решения этого пути")).toBeVisible();
  await page
    .getByRole("button", { name: /Выбран действующей политикой/ })
    .click();
  await expect(
    page.getByText("Почему выбран путь", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("table", { name: "Альтернативы выбора" }),
  ).toContainText("Предпочтён отдельный серверный IP");
  await expect(
    page.getByRole("table", { name: "Альтернативы выбора" }),
  ).toContainText("Общий IP отложен после отдельных путей");
  await page.getByRole("button", { name: "Закрыть", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Закрыть", exact: true }),
  ).not.toBeVisible();
  await page.getByRole("button", { name: "Следующие IP" }).click();
  await expect(page.getByText("IP-группы 5-5 из 5")).toBeVisible();
  // Unknown DNS must never merge unrelated routes just because group is blank.
  await page.getByRole("button", { name: "Предыдущие IP" }).click();
  await page.getByLabel("Поиск профиля / IP").fill("unknown-");
  await expect(
    flow.locator('[data-model-id^="ip-group:unknown:"]'),
  ).toHaveCount(2);
  expect(writes).toEqual([]);
  expect(errors).toEqual([]);
  await page.screenshot({
    path: "/reports/reactflow-unknown.png",
    fullPage: true,
  });
});

test("real Web Worker, SSE metric updates keep layout/viewport, unmount disposes worker", async ({
  page,
}) => {
  const value = snapshot();
  const workers: any[] = [];
  page.on("worker", (w) => workers.push(w));
  await page.addInitScript(() => {
    const Native = window.EventSource;
    class DemoSource extends EventTarget {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;
      readyState = 1;
      onerror: any = null;
      url: string;
      constructor(url: string) {
        super();
        this.url = url;
        if (url.includes("/topology/events")) (window as any).demoStream = this;
      }
      close() {
        this.readyState = 2;
      }
    }
    (window as any).EventSource = DemoSource;
    (window as any).originalEventSource = Native;
  });
  await fixture(page, value);
  expect(workers.length).toBe(1);
  expect(workers[0].url()).toMatch(/elk-worker.*\.js/);
  const chart = page.locator(
    '[data-model-id="ip-group:203.0.113.10|203.0.113.11"]',
  );
  await expect(chart.getByTestId("spark-speed")).toContainText("нет данных");
  await expect(chart.getByTestId("spark-rtt")).toContainText("42 мс");
  const initial = await page
    .locator(".react-flow__viewport")
    .getAttribute("style");
  const positions = await page
    .locator(".react-flow__node")
    .evaluateAll((ns) =>
      ns.map((n) => [
        n.getAttribute("data-id"),
        (n as HTMLElement).style.transform,
      ]),
    );
  await page.evaluate((v) => {
    const f = {
      ...v.workers[0],
      sequence: 2,
      at: new Date(Date.parse(v.workers[0].at) + 10000).toISOString(),
      routes: v.workers[0].routes.map((r) => ({
        ...r,
        up: r.up + 10000,
        down: r.down + 5000000,
      })),
    };
    (window as any).demoStream.dispatchEvent(
      new MessageEvent("frame", {
        data: JSON.stringify({ cursor: "test:2", worker: f.worker, frame: f }),
      }),
    );
  }, value);
  await expect(
    page.locator('[data-model-id="ip-group:203.0.113.10|203.0.113.11"]'),
  ).toContainText(/↓\s*12/);
  await expect(chart.locator('path[data-series="down"]')).toHaveAttribute(
    "d",
    /M/,
  );
  await expect(chart.getByTestId("spark-speed")).toContainText(/↓\s*12/);
  await expect(page.getByTestId("topology-flow")).toHaveAttribute(
    "data-layout-count",
    "1",
  );
  expect(
    await page.locator(".react-flow__viewport").getAttribute("style"),
  ).toBe(initial);
  expect(
    await page
      .locator(".react-flow__node")
      .evaluateAll((ns) =>
        ns.map((n) => [
          n.getAttribute("data-id"),
          (n as HTMLElement).style.transform,
        ]),
      ),
  ).toEqual(positions);
  await page.screenshot({
    path: "/reports/reactflow-desktop.png",
    fullPage: true,
  });
  const ms = await page
    .getByTestId("topology-flow")
    .getAttribute("data-layout-ms");
  fs.writeFileSync(
    "/reports/reactflow-layout-measurement.json",
    JSON.stringify(
      {
        nodes: positions.length,
        initial_layout_ms: Number(ms),
        heap: await page.evaluate(
          () => (performance as any).memory?.usedJSHeapSize,
        ),
        note: "Browser heap includes full UI, not isolated ELK memory",
      },
      null,
      2,
    ),
  );
  await page.goto("/#connections");
  await expect.poll(() => page.workers().length).toBe(0);
});

test("mobile keyboard, reduced motion and empty state", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await fixture(page);
  await page.locator(".react-flow__node").first().focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("button", { name: "Закрыть", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Закрыть", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Закрыть", exact: true }),
  ).not.toBeVisible();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.getByLabel("Поиск профиля / IP").fill("no-such-profile");
  await expect(
    page.getByText("Нет элементов для выбранных фильтров."),
  ).toBeVisible();
  await expect(page.locator(".react-flow__node")).toHaveCount(0);
  expect(errors).toEqual([]);
  await page.screenshot({
    path: "/reports/reactflow-mobile.png",
    fullPage: true,
  });
});

test("worker failure falls back visibly and can retry", async ({ page }) => {
  await page.route("**/assets/elk-worker*.js", (r) => r.abort());
  const value = snapshot();
  await page.route("**/api/v1/**", (r) => {
    const path = new URL(r.request().url()).pathname;
    if (path.includes("topology/events") || path.includes("events/stream"))
      return r.fulfill({
        contentType: "text/event-stream",
        body: ": keepalive\n\n",
      });
    return r.fulfill({
      json: path.endsWith("topology/snapshot")
        ? value
        : { csrf: "test", items: [], counts: {} },
    });
  });
  await page.goto("/#topology");
  await expect(page.getByText(/Используется простая сетка/)).toBeVisible({
    timeout: 40000,
  });
  expect(await page.locator(".react-flow__node").count()).toBeGreaterThan(0);
  await page.unroute("**/assets/elk-worker*.js");
  await page.getByRole("button", { name: "Перестроить", exact: true }).click();
  await expect(page.getByTestId("topology-flow")).toHaveAttribute(
    "data-layout-count",
    "1",
  );
  await expect(page.getByText(/Используется простая сетка/)).toHaveCount(0);
});

test("bounded 512 observed routes and paged projection", async ({ page }) => {
  const value = snapshot(),
    template = value.workers[0].routes[0];
  value.workers[0].routes = Array.from({ length: 512 }, (_, i) => ({
    ...template,
    id: "large-" + i,
    engine: "large-engine-" + i,
    server_ips: ["192.0.2." + ((i % 240) + 1)],
  }));
  value.workers[0].catalog_size = 10000;
  await fixture(page, value);
  await expect(page.getByText(/Показаны 60 из 512/)).toBeVisible();
  expect(await page.locator(".react-flow__node").count()).toBeLessThanOrEqual(
    7,
  );
});

test("readable defaults, retained collapsible filters, native fullscreen and inspector", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await fixture(page);
  const root = page.getByTestId("traffic-topology");
  const flow = page.getByTestId("topology-flow");
  const readable = await flow
    .locator(".react-flow__node")
    .first()
    .evaluate((n) => {
      const transform = new DOMMatrix(
        getComputedStyle(n.closest(".react-flow__viewport")!).transform,
      );
      const title = n.querySelector(".MuiTypography-body1")!;
      return {
        width: n.getBoundingClientRect().width,
        text: parseFloat(getComputedStyle(title).fontSize) * transform.a,
      };
    });
  expect(readable.width).toBeGreaterThanOrEqual(299);
  expect(readable.text).toBeGreaterThanOrEqual(15.9);
  await expect
    .poll(async () => {
      const host = (await flow.boundingBox())!;
      const card = (await flow
        .locator('[data-model-id^="ip-group:"]')
        .first()
        .boundingBox())!;
      return (
        card.x >= host.x &&
        card.x + card.width <= host.x + host.width &&
        card.y >= host.y
      );
    })
    .toBe(true);
  await page.getByLabel("Поиск профиля / IP").fill("route-");
  await page
    .getByRole("button", { name: "Свернуть фильтры (1)", exact: true })
    .click();
  await expect(page.getByLabel("Поиск профиля / IP")).not.toBeVisible();
  await page.getByRole("button", { name: "Полный экран", exact: true }).click();
  await expect
    .poll(() =>
      page.evaluate(() =>
        document.fullscreenElement?.getAttribute("data-testid"),
      ),
    )
    .toBe("traffic-topology");
  await expect(root).toHaveClass(/rw-topology-fullscreen/);
  expect((await root.boundingBox())?.height).toBe(1000);
  expect((await flow.boundingBox())!.height).toBeGreaterThan(650);
  await page
    .getByRole("button", { name: "Показать фильтры (1)", exact: true })
    .click();
  await expect(page.getByLabel("Поиск профиля / IP")).toHaveValue("route-");
  await page.getByLabel("Окно скорости").click();
  await root.getByRole("option", { name: "30 с", exact: true }).click();
  await expect(
    page.getByRole("combobox", { name: /Окно скорости/ }),
  ).toContainText("30 с");
  await page
    .getByRole("button", { name: "Свернуть фильтры (1)", exact: true })
    .click();
  await page.screenshot({ path: "/reports/topology-fullscreen-readable.png" });
  // The inspector must stay inside the fullscreen element (including MUI portals).
  await page.getByRole("button", { name: "Вместить", exact: true }).click();
  const group = flow.locator(
    '[data-model-id="ip-group:203.0.113.10|203.0.113.11"]',
  );
  await group.getByRole("button", { name: "Раскрыть профили (3)" }).click();
  await group.locator(".rw-card-title").first().click();
  await expect(root.getByText("Последние решения этого пути")).toBeVisible();
  await root.getByRole("button", { name: "Закрыть", exact: true }).click();
  await page.screenshot({ path: "/reports/topology-fullscreen.png" });
  await page
    .getByRole("button", { name: "Выйти из полного экрана", exact: true })
    .click();
  await expect
    .poll(() => page.evaluate(() => !!document.fullscreenElement))
    .toBe(false);
  await expect(root).not.toHaveClass(/rw-topology-fullscreen/);
  await page.getByRole("button", { name: "Полный экран", exact: true }).click();
  await expect(root).toHaveClass(/rw-topology-fullscreen/);
  await page.keyboard.press("Escape");
  await expect(root).not.toHaveClass(/rw-topology-fullscreen/);
  await page.getByRole("button", { name: "100%", exact: true }).click();
  await expect
    .poll(async () =>
      page
        .locator(".react-flow__viewport")
        .evaluate((e) => new DOMMatrix(getComputedStyle(e).transform).a),
    )
    .toBe(1);
});

test("fullscreen fallback on mobile, Escape and unmount restore page scrolling", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.addInitScript(() => {
    Object.defineProperty(Element.prototype, "requestFullscreen", {
      configurable: true,
      value: undefined,
    });
  });
  await fixture(page);
  await page
    .getByRole("button", { name: "Свернуть фильтры", exact: true })
    .click();
  await page.getByRole("button", { name: "Полный экран", exact: true }).click();
  await expect(page.getByTestId("traffic-topology")).toHaveClass(
    /rw-topology-fullscreen/,
  );
  expect(
    (await page.getByTestId("topology-flow").boundingBox())!.height,
  ).toBeGreaterThan(400);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(
    390,
  );
  await page.screenshot({ path: "/reports/topology-fullscreen-mobile.png" });
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("traffic-topology")).not.toHaveClass(
    /rw-topology-fullscreen/,
  );
  expect(await page.evaluate(() => document.body.style.overflow)).not.toBe(
    "hidden",
  );
  await page.getByRole("button", { name: "Полный экран", exact: true }).click();
  await page.evaluate(() => {
    location.hash = "#connections";
  });
  await expect(page.getByTestId("traffic-topology")).toHaveCount(0);
  expect(await page.evaluate(() => document.body.style.overflow)).not.toBe(
    "hidden",
  );
});

test("chart semantics: counters, reset/gap, epoch boundaries, median and independent IP sums", () => {
  const sample = snapshot().workers[0];
  const first = { ...sample, at: "2026-10-21T12:00:00Z", generation: 1 };
  const second = {
    ...first,
    sequence: 2,
    at: "2026-10-21T12:00:10Z",
    routes: first.routes.map((r) => ({
      ...r,
      up: r.up + 1250000,
      down: r.down + 2500000,
      rtt_us: 80000,
    })),
  };
  const history = routeHistories([first, second], 10);
  expect(history.get("engine-route-1")?.[0].down).toBeNull();
  expect(history.get("engine-route-1")?.[1]).toMatchObject({
    up: 1,
    down: 2,
    rtt: 80,
  });
  const reset = {
    ...second,
    sequence: 3,
    at: "2026-10-21T12:00:20Z",
    routes: second.routes.map((r) => ({ ...r, up: 0, down: 0, rtt_us: 0 })),
  };
  expect(
    routeHistories([first, second, reset], 10).get("engine-route-1")?.[2],
  ).toMatchObject({ up: null, down: null, rtt: null });
  const gap = { ...second, sequence: 4, at: "2026-10-21T12:01:00Z" };
  expect(
    routeHistories([second, gap], 10).get("engine-route-1")?.[1].down,
  ).toBeNull();
  const generation = { ...second, generation: 2 };
  expect(
    routeHistories([first, generation], 10).get("engine-route-1")?.[1].down,
  ).toBeNull();
  const row = (id: string, rtt: number, shift = 0) =>
    ({
      worker: "w",
      epoch: "epoch",
      engine: id,
      history: [{ at: 10000 + shift, up: 1, down: 2, rtt }],
    }) as any;
  const member = row("one", 20);
  expect(aggregateHistory([member, member, row("two", 80)])[0]).toMatchObject({
    up: 2,
    down: 4,
    rtt: 50,
  });
  expect(aggregateHistory([member, row("two", 80, 500)]).at(-1)).toMatchObject({
    down: 4,
    rtt: 50,
  });
  const segments = [
    { at: 0, up: 1, down: 1, rtt: 20 },
    { at: 1000, up: null, down: null, rtt: null },
    { at: 2000, up: 1, down: 1, rtt: 40 },
  ];
  expect(sparkPath(segments, "down", 1, 60000)).not.toContain("L");
  expect(aggregateHistory([])).toEqual([]);
});

test("every card has accessible charts, unknown nodes stay empty and profiles are not clipped", async ({
  page,
}) => {
  await fixture(page);
  const flow = page.getByTestId("topology-flow");
  await page.getByRole("button", { name: "Вместить", exact: true }).click();
  expect(await flow.getByTestId("route-sparklines").count()).toBe(
    await flow.getByTestId("topology-card").count(),
  );
  await expect(
    flow.locator('[data-model-id="clients"]').getByTestId("spark-speed"),
  ).toContainText("Не измеряется");
  const group = flow.locator(
    '[data-model-id="ip-group:203.0.113.10|203.0.113.11"]',
  );
  await group.getByRole("button", { name: "Раскрыть профили (3)" }).click();
  await expect(flow.locator('[data-model-id^="profile:"]')).toHaveCount(3);
  await expect(group.getByTestId("spark-rtt")).toContainText("медиана");
  const bounds = await flow.getByTestId("topology-card").evaluateAll((cards) =>
    cards.map((c) => ({
      id: c.getAttribute("data-model-id"),
      client: c.clientHeight,
      content: c.scrollHeight,
    })),
  );
  expect(bounds.filter((b) => b.content > b.client + 1)).toEqual([]);
  await page
    .getByRole("button", { name: "Свернуть фильтры", exact: true })
    .click();
  await page.getByRole("button", { name: "Полный экран", exact: true }).click();
  await page.getByRole("button", { name: "Вместить", exact: true }).click();
  await page.screenshot({ path: "/reports/topology-charts.png" });
});

test("wide low cards fit four IP rows on desktop at readable scale", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1920, height: 1080 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await fixture(page);
  await page
    .getByRole("button", { name: "Свернуть фильтры", exact: true })
    .click();
  await page.getByRole("button", { name: "Полный экран", exact: true }).click();
  await expect(page.getByTestId("traffic-topology")).toHaveClass(
    /rw-topology-fullscreen/,
  );
  const flow = page.getByTestId("topology-flow");
  const cards = flow.locator('[data-model-id^="ip-group:"]');
  await expect(cards).toHaveCount(4);
  const measure = async () => {
    const host = (await flow.boundingBox())!;
    const boxes = await cards.evaluateAll((cs) =>
      cs.map((c) => {
        const b = c.getBoundingClientRect();
        return {
          x: b.x,
          y: b.y,
          right: b.right,
          bottom: b.bottom,
          width: b.width,
          height: b.height,
          client: c.clientHeight,
          content: c.scrollHeight,
        };
      }),
    );
    expect(boxes.every((b) => b.width >= 479 && b.height <= 166)).toBe(true);
    expect(boxes.every((b) => b.content <= b.client + 1)).toBe(true);
    expect(
      Math.max(...boxes.map((b) => b.bottom)) -
        Math.min(...boxes.map((b) => b.y)),
    ).toBeLessThanOrEqual(710);
    expect(
      boxes.every(
        (b) =>
          b.x >= host.x &&
          b.right <= host.x + host.width &&
          b.y >= host.y &&
          b.bottom <= host.y + host.height,
      ),
    ).toBe(true);
  };
  await measure();
  await page.screenshot({ path: "/reports/topology-compact-desktop.png" });
  await page
    .getByRole("button", { name: "Выйти из полного экрана", exact: true })
    .click();
  await expect(page.getByTestId("traffic-topology")).not.toHaveClass(
    /rw-topology-fullscreen/,
  );
  await page.setViewportSize({ width: 3592, height: 1956 });
  await page.getByRole("button", { name: "Полный экран", exact: true }).click();
  await expect(page.getByTestId("traffic-topology")).toHaveClass(
    /rw-topology-fullscreen/,
  );
  await measure();
  await page.screenshot({ path: "/reports/topology-compact-wide.png" });
});
