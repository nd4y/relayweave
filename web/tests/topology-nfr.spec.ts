import { test, expect, request } from "@playwright/test";
import net from "node:net";
import fs from "node:fs";

test("5000 actual tunnels and conservative time to visible routing event", async ({
  page,
}) => {
  test.setTimeout(420000);
  const sockets: net.Socket[] = [];
  const allSockets: net.Socket[] = [];
  const failures: string[] = [];
  const create = () =>
    new Promise<void>((resolve, reject) => {
      const socket = net.connect(38080, "127.0.0.1");
      allSockets.push(socket);
      let header = "";
      socket.setTimeout(30000);
      socket.on("error", (e) => {
        failures.push(e.message);
        reject(e);
      });
      socket.on("timeout", () => {
        socket.destroy();
        reject(new Error("tunnel setup timeout"));
      });
      const read = (data: Buffer) => {
        header += data.toString();
        if (header.includes("\r\n\r\n")) {
          socket.off("data", read);
          if (!header.startsWith("HTTP/1.1 200")) {
            socket.destroy();
            reject(new Error("CONNECT rejected"));
            return;
          }
          socket.setTimeout(0);
          socket.once("data", (data) => {
            if (data.toString() !== "topology-echo\n") {
              reject(new Error("echo mismatch"));
              return;
            }
            sockets.push(socket);
            resolve();
          });
          socket.write("topology-echo\n");
        }
      };
      socket.on("data", read);
      socket.once("connect", () =>
        socket.write(
          "CONNECT origin:9001 HTTP/1.1\r\nHost: origin:9001\r\n\r\n",
        ),
      );
    });
  const context = await request.newContext({
    proxy: { server: "http://127.0.0.1:38081" },
  });
  try {
    for (let n = 0; n < 5000; n += 32)
      await Promise.all(Array.from({ length: Math.min(32, 5000 - n) }, create));
    expect(sockets.length).toBe(5000);
    await page.goto("/#topology");
    await page
      .getByLabel("Пароль администратора")
      .fill(process.env.ADMIN_PASSWORD!);
    await page.getByRole("button", { name: "Войти", exact: true }).click();
    const root = page.getByTestId("traffic-topology");
    await expect(root.getByText("Live", { exact: true })).toBeVisible();
    await root.getByLabel("Представление").click();
    await page
      .getByRole("option", { name: "Решения и события", exact: true })
      .click();
    const batches: number[] = [];
    await page.evaluate(() => {
      const state = {
        started: Date.now(),
        seen: new Set(
          Array.from(document.querySelectorAll("[data-decision-id]"), (n) =>
            n.getAttribute("data-decision-id"),
          ),
        ),
        values: [] as {
          id: string;
          selection: number;
          finished: number;
          visible: number;
        }[],
      };
      (window as any).__topologyPaint = state;
      const observer = new MutationObserver(() => {
        for (const node of document.querySelectorAll("[data-decision-id]")) {
          const id = node.getAttribute("data-decision-id")!;
          if (state.seen.has(id)) continue;
          state.seen.add(id);
          const selection = Date.parse(node.getAttribute("data-selection-at")!);
          const finished = Date.parse(node.getAttribute("data-finished-at")!);
          requestAnimationFrame(() =>
            requestAnimationFrame(() => {
              if (
                selection >= state.started &&
                node.isConnected &&
                node.getBoundingClientRect().height > 0 &&
                node.getBoundingClientRect().top >= 0 &&
                node.getBoundingClientRect().bottom <= window.innerHeight
              )
                state.values.push({
                  id,
                  selection,
                  finished,
                  visible: Date.now(),
                });
            }),
          );
        }
      });
      observer.observe(
        document.querySelector('[data-testid="traffic-topology"]')!,
        { childList: true, subtree: true },
      );
    });
    const generationFailures: string[] = [];
    try {
      for (let n = 0; n < 32; n++) {
        const started = Date.now();
        for (let i = 0; i < 16; i++) {
          const response = await context.get("http://origin:9000/healthz", {
            headers: { Connection: "close" },
          });
          expect(response.ok()).toBeTruthy();
          await response.dispose();
        }
        batches.push((Date.now() - started) / 1000);
        await page.waitForTimeout(2200);
      }
    } catch (error) {
      generationFailures.push(
        error instanceof Error
          ? error.message.split("\n")[0]
          : "request generation failed",
      );
    }
    await page.waitForTimeout(2200);
    if (!generationFailures.length)
      await expect
        .poll(
          async () =>
            page.evaluate(() => (window as any).__topologyPaint.values.length),
          { timeout: 6000 },
        )
        .toBeGreaterThanOrEqual(32);
    const paints = (await page.evaluate(
      () => (window as any).__topologyPaint.values,
    )) as {
      id: string;
      selection: number;
      finished: number;
      visible: number;
    }[];
    const times = paints
      .map((v) => (v.visible - v.selection) / 1000)
      .sort((a, b) => a - b);
    const finishedTimes = paints
      .map((v) => (v.visible - v.finished) / 1000)
      .sort((a, b) => a - b);
    expect(times.every((n) => Number.isFinite(n) && n >= 0)).toBe(true);
    expect(finishedTimes.every((n) => Number.isFinite(n) && n >= 0)).toBe(true);
    const result = await page.request.get("/api/v1/topology/snapshot");
    const snapshot = await result.json();
    const active = snapshot.workers
      .flatMap((w: any) => w.routes)
      .reduce((sum: number, r: any) => sum + r.active, 0);
    expect(active).toBeGreaterThanOrEqual(5000);
    times.sort((a, b) => a - b);
    const p95 = times[Math.floor(times.length * 0.95)];
    const report = {
      actual_tunnels: sockets.length,
      active_observed: active,
      samples: times.length,
      selection_to_visible_p95_seconds: p95,
      finished_to_visible_p95_seconds:
        finishedTimes[Math.floor(finishedTimes.length * 0.95)],
      request_generation_seconds: batches,
      event_ids: paints.map((v) => v.id),
      latencies: times,
      generation_failures: generationFailures,
      passed:
        failures.length === 0 &&
        !generationFailures.length &&
        times.length >= 32 &&
        p95 <= 2,
      measurement:
        "actual immutable worker selection/finished timestamps to new DOM decision node plus two animation frames, observed continuously during request generation; containers and browser share the build-01 clock; batch-generation times reported separately",
    };
    fs.writeFileSync(
      "/reports/topology-visible-5000.json",
      JSON.stringify(report, null, 2),
    );
    await page.screenshot({
      path: "/reports/topology-visible-5000.png",
      fullPage: true,
    });
    expect(failures).toEqual([]);
    expect(generationFailures).toEqual([]);
    expect(times.length).toBeGreaterThanOrEqual(32);
    expect(p95).toBeLessThanOrEqual(2);
  } finally {
    for (const socket of allSockets) socket.destroy();
    await context.dispose();
  }
});
