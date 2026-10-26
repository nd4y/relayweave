import { test, expect } from "@playwright/test";
import net from "node:net";

// HTTP forwarding may reuse its upstream connection. Explicit CONNECT makes
// fresh selector decisions without touching external destinations/providers.
async function seedDecisions() {
  for (let i = 0; i < 16; i++)
    await new Promise<void>((resolve, reject) => {
      const socket = net.connect(38080, "127.0.0.1");
      let stage = 0,
        buffer = "";
      socket.setTimeout(8000);
      socket.on("connect", () =>
        socket.write(
          "CONNECT origin:9000 HTTP/1.1\r\nHost: origin:9000\r\n\r\n",
        ),
      );
      socket.on("data", (chunk) => {
        buffer += chunk.toString();
        if (stage === 0 && buffer.includes("\r\n\r\n")) {
          if (!buffer.startsWith("HTTP/1.1 200")) {
            socket.destroy();
            reject(new Error("Lab CONNECT rejected"));
            return;
          }
          stage = 1;
          buffer = "";
          socket.write(
            "GET /healthz HTTP/1.1\r\nHost: origin\r\nConnection: close\r\n\r\n",
          );
        }
      });
      socket.on("end", () => {
        socket.destroy();
        if (stage === 1 && buffer.startsWith("HTTP/1.1 200")) resolve();
        else reject(new Error("Lab origin response missing"));
      });
      socket.on("error", reject);
      socket.on("timeout", () => {
        socket.destroy();
        reject(new Error("Lab CONNECT timed out"));
      });
    });
}

test("synthetic large catalogue projection remains bounded, separate from real protocol acceptance", async ({
  page,
}) => {
  await page.goto("/");
  await page
    .getByLabel("Пароль администратора")
    .fill(process.env.ADMIN_PASSWORD!);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Обзор", exact: true }),
  ).toBeVisible();
  const response = await page.request.get("/api/v1/topology/snapshot");
  const base = await response.json();
  const template = base.workers[0];
  expect(template.routes.length).toBeGreaterThan(0);
  const synthetic = {
    ...base,
    workers: Array.from({ length: 4 }, (_, w) => ({
      ...template,
      worker: "worker-synthetic-" + w,
      catalog_size: 10000,
      routes: Array.from({ length: 128 }, (_, n) => ({
        ...template.routes[0],
        id: `synthetic-${w}-${n}`,
        engine: `synthetic-engine-${w}-${n}`,
        group: `synthetic-group-${n % 32}`,
        active: n % 5,
        server_host: "synthetic.invalid",
        server_ips: ["192.0.2.1"],
      })),
    })),
  };
  await page.route("**/api/v1/topology/snapshot", (route) =>
    route.fulfill({ json: synthetic }),
  );
  await page.route("**/api/v1/topology/events?*", (route) => route.abort());
  await page.goto("/#topology");
  const root = page.getByTestId("traffic-topology");
  await expect(root.getByText(/Показаны 60 из 512/)).toBeVisible();
  const graph = root.getByRole("group", {
    name: "Интерактивная карта трафика",
  });
  expect(await graph.locator(".react-flow__node").count()).toBeLessThanOrEqual(
    1500,
  );
  expect(await graph.locator("rect").count()).toBeLessThanOrEqual(500);
  expect(
    await graph.locator("g[role=button] path").count(),
  ).toBeLessThanOrEqual(1000);
  await page.screenshot({
    path: "/reports/topology-synthetic-bounds.png",
    fullPage: true,
  });
});

test("real worker topology, selection, filters, pause, table, decisions, mobile and reduced motion", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/#topology");
  await page
    .getByLabel("Пароль администратора")
    .fill(process.env.ADMIN_PASSWORD!);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Карта трафика", exact: true }),
  ).toBeVisible();
  const root = page.getByTestId("traffic-topology");
  await expect(root.getByText("Live", { exact: true })).toBeVisible();
  await expect(
    root.getByRole("group", {
      name: "Интерактивная карта трафика",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    root.getByRole("button", { name: /группа серверных IP: / }).first(),
  ).toBeVisible();
  await page.screenshot({
    path: "/reports/topology-traffic-desktop.png",
    fullPage: true,
  });
  const profile = root
    .getByRole("button", { name: /группа серверных IP: / })
    .first();
  await profile.focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByText(/^(Пример наблюдаемого пути|Наблюдаемый путь)/),
  ).toBeVisible();
  await page.getByRole("button", { name: "Раскрыть группу и соседей" }).click();
  await expect(
    page.getByRole("button", { name: "Свернуть группу" }),
  ).toBeVisible();
  await page.screenshot({
    path: "/reports/topology-selection.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Закрыть", exact: true }).click();
  await root.getByRole("button", { name: "Пауза", exact: true }).click();
  await expect(
    root.getByRole("button", { name: "Продолжить", exact: true }),
  ).toBeVisible();
  await page.waitForTimeout(1300);
  await root.getByRole("button", { name: "Продолжить", exact: true }).click();
  await expect(root.getByText("Live", { exact: true })).toBeVisible();
  await root.getByLabel("Поиск профиля / IP").fill("not-a-real-profile");
  await expect(
    root.getByText("Нет элементов для выбранных фильтров."),
  ).toBeVisible();
  await root.getByLabel("Поиск профиля / IP").clear();
  await root.getByLabel("Таблица", { exact: true }).check();
  await expect(
    root.getByRole("table", { name: "Табличная карта трафика" }),
  ).toBeVisible();
  await page.screenshot({
    path: "/reports/topology-table.png",
    fullPage: true,
  });
  await root.getByLabel("Таблица", { exact: true }).uncheck();
  await root.getByLabel("Представление").click();
  await page
    .getByRole("option", { name: "Подписки и пути", exact: true })
    .click();
  await expect(
    root.getByRole("button", { name: /наблюдаемый A\/AAAA/ }).first(),
  ).toBeVisible();
  await page.screenshot({
    path: "/reports/topology-dependencies.png",
    fullPage: true,
  });
  await root.getByLabel("Представление").click();
  await page
    .getByRole("option", { name: "Решения и события", exact: true })
    .click();
  await expect(
    root.getByText("Фактические решения", { exact: true }),
  ).toBeVisible();
  const item = root
    .getByRole("button")
    .filter({ hasText: /Выбран действующей политикой/ })
    .first();
  await seedDecisions();
  await expect(item).toBeVisible();
  {
    await item.click();
    await expect(
      page.getByText("Почему выбран путь", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("table", { name: "Альтернативы выбора" }),
    ).toBeVisible();
    await page.screenshot({
      path: "/reports/topology-decision.png",
      fullPage: true,
    });
    await page.getByRole("button", { name: "Закрыть", exact: true }).click();
  }
  await root.getByRole("button", { name: "История агрегатов" }).click();
  await expect(root.getByText(/Сохранённые агрегаты/)).toBeVisible();
  await root.getByRole("button", { name: "Закрыть историю" }).click();
  await root.getByLabel("Представление").click();
  await page.getByRole("option", { name: "Трафик", exact: true }).click();
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: "/reports/topology-mobile.png",
    fullPage: true,
  });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(391);
  expect(await root.locator(".topology-flow").count()).toBe(0);
  expect(errors).toEqual([]);
});

test("stream disconnect retains graph and reconnects without routing mutation", async ({
  page,
}) => {
  await page.goto("/#topology");
  await page
    .getByLabel("Пароль администратора")
    .fill(process.env.ADMIN_PASSWORD!);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  const root = page.getByTestId("traffic-topology");
  await expect(root.getByText("Live", { exact: true })).toBeVisible();
  await page.context().setOffline(true);
  await page.waitForTimeout(7000);
  await expect(
    root.getByRole("group", { name: "Интерактивная карта трафика" }),
  ).toBeVisible();
  await expect(root.getByText(/Последняя схема сохранена/)).toBeVisible();
  await page.screenshot({
    path: "/reports/topology-stale.png",
    fullPage: true,
  });
  await page.context().setOffline(false);
  await expect(root.getByText("Live", { exact: true })).toBeVisible({
    timeout: 35000,
  });
});
