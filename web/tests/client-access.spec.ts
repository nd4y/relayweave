import { test, expect, request } from "@playwright/test";
import fs from "node:fs";
async function login(page: any) {
  await page.goto("/#connections");
  await page
    .getByLabel("Пароль администратора")
    .fill(process.env.ADMIN_PASSWORD!);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await expect(
    page
      .getByTestId("client-access")
      .getByRole("button", { name: "Подготовить конфиг" }),
  ).toBeEnabled();
}
test("actual client export clipboard, autonomous subscription and revoke", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await login(page);
  const root = page.getByTestId("client-access");
  await root.getByRole("button", { name: "Подготовить конфиг" }).click();
  await expect(root.getByLabel("Конфигурация подключения")).toHaveValue(
    /Данные скрыты/,
  );
  await root.getByRole("button", { name: "Копировать для Throne" }).click();
  await expect(page.getByText("Скопировано", { exact: true })).toBeVisible();
  const link = await page.evaluate(() => navigator.clipboard.readText());
  expect(link.startsWith("throne://add/")).toBe(true);
  const native = JSON.parse(
    Buffer.from(link.slice("throne://add/".length), "base64url").toString(),
  );
  expect(["http", "socks"]).toContain(native.type);
  expect(native.server).toBe("127.0.0.1");
  expect([38080, 38081, 31080]).toContain(native.server_port);
  await root.getByLabel(/Разрешаю включить данные доступа/).check();
  await root.getByRole("button", { name: "Создать ссылку подписки" }).click();
  await expect(
    root.getByRole("button", { name: "Копировать ссылку подписки" }),
  ).toBeVisible();
  await root
    .getByRole("button", { name: "Копировать ссылку подписки" })
    .click();
  const subscription = await page.evaluate(() =>
    navigator.clipboard.readText(),
  );
  expect(new URL(subscription).pathname).toMatch(
    /^\/client-sub\/[a-f0-9]{64}$/,
  );
  expect(subscription).not.toContain(process.env.ADMIN_PASSWORD!);
  const standalone = await request.newContext();
  try {
    const response = await standalone.get(subscription);
    expect(response.status()).toBe(200);
    expect((await response.json()).outbounds).toHaveLength(1);
    await root.getByRole("button", { name: "Отозвать", exact: true }).click();
    await expect
      .poll(async () => (await standalone.get(subscription)).status())
      .toBe(404);
  } finally {
    await standalone.dispose();
  }
  await page.screenshot({ path: "/reports/client-access.png", fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(391);
  await page.screenshot({
    path: "/reports/client-access-mobile.png",
    fullPage: true,
  });
});
test("clipboard unavailable has feedback and JSON download fallback", async ({
  page,
}) => {
  await page.addInitScript(() =>
    Object.defineProperty(navigator, "clipboard", {
      value: undefined,
      configurable: true,
    }),
  );
  await login(page);
  expect(await page.evaluate(()=>typeof navigator.clipboard)).toBe("undefined");
  const root = page.getByTestId("client-access");
  await root.getByRole("button", { name: "Подготовить конфиг" }).click();
  await root.getByRole("button", { name: "Копировать JSON" }).click();
  await expect(page.getByText(/Буфер недоступен/)).toBeVisible();
  const pending = page.waitForEvent("download");
  await root.getByRole("button", { name: "Скачать конфиг" }).click();
  const downloaded = await pending;
  expect(downloaded.suggestedFilename()).toBe("relayweave-client.json");
  const data = JSON.parse(fs.readFileSync((await downloaded.path())!, "utf-8"));
  expect(data.outbounds).toHaveLength(1);
  expect(data.outbounds[0].server).toBe("127.0.0.1");
});
