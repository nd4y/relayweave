import { test, expect, type Page } from "@playwright/test";

const hint = "https://example.invalid/••••";
const secret =
  "https://example.invalid/subscription/private-token?key=private-value";
async function fixture(page: Page, delay = 0, fail = false) {
  let reveals = 0;
  let saved: any;
  const source = {
    id: "source-url",
    kind: "sources",
    name: "URL fixture",
    version: 1,
    updated_at: new Date().toISOString(),
    data: {
      url: "",
      url_hint: hint,
      url_configured: true,
      enabled: true,
      profile_count: 1,
      core_version: "26.10.17",
      interval_seconds: 300,
    },
  };
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/v1/", "");
    if (path === "events/stream") {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": fixture\n\n",
      });
      return;
    }
    let body: unknown = { items: [], total: 0 };
    if (path === "auth/session")
      body = { authenticated: true, csrf: "fixture-csrf" };
    if (path === "sources") body = { items: [source], total: 1 };
    if (path === "overview")
      body = { counts: {}, sources: {}, workers: [], events: [], settings: {} };
    if (path === "sources/source-url/reveal-url") {
      reveals++;
      expect(request.method()).toBe("POST");
      expect(request.headers()["x-csrf-token"]).toBe("fixture-csrf");
      if (delay) await new Promise((resolve) => setTimeout(resolve, delay));
      await route.fulfill({
        status: fail ? 503 : 200,
        contentType: "application/json",
        body: JSON.stringify(
          fail
            ? { error: { code: "source_url_unavailable" } }
            : { url: secret },
        ),
      });
      return;
    }
    if (path === "sources/source-url" && request.method() === "PATCH") {
      saved = request.postDataJSON();
      body = source;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
  await page.goto("/#sources");
  await expect(
    page.getByRole("heading", { name: "Подписки", exact: true }),
  ).toBeVisible();
  return { reveals: () => reveals, saved: () => saved };
}

test("card reveals only on click, hides and clears the secret", async ({
  page,
}) => {
  const state = await fixture(page);
  const card = page.locator(".object-card").filter({ hasText: "URL fixture" });
  const field = card.getByLabel("URL подписки");
  await expect(field).toHaveValue(hint);
  expect(state.reveals()).toBe(0);
  await card
    .getByRole("button", { name: "Показать адрес подписки", exact: true })
    .click();
  await expect(field).toHaveValue(secret);
  expect(await field.getAttribute("readonly")).not.toBeNull();
  await card
    .getByRole("button", { name: "Скрыть адрес подписки", exact: true })
    .click();
  await expect(field).toHaveValue(hint);
  expect(
    await page.evaluate(() => JSON.stringify([localStorage, sessionStorage])),
  ).not.toContain("private-token");
});

test("editor reveal does not replace or save the stored URL implicitly", async ({
  page,
}) => {
  const state = await fixture(page);
  await page.getByRole("button", { name: "Редактировать URL fixture" }).click();
  const dialog = page.getByRole("dialog");
  const field = dialog.getByLabel("URL подписки");
  await expect(field).toHaveValue("");
  await dialog
    .getByRole("button", { name: "Показать адрес подписки", exact: true })
    .click();
  await expect(field).toHaveValue(secret);
  await dialog.getByRole("button", { name: "Сохранить", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(state.saved()?.data.url || "").toBe("");
});

test("new typed URL toggles locally and mobile eye stays within the field", async ({
  page,
}) => {
  const state = await fixture(page);
  await page.setViewportSize({ width: 360, height: 900 });
  await page
    .getByRole("button", { name: "Добавить", exact: true })
    .first()
    .click();
  const dialog = page.getByRole("dialog");
  const field = dialog.getByLabel("URL подписки", { exact: false });
  await field.fill(secret);
  await dialog
    .getByRole("button", { name: "Показать адрес подписки", exact: true })
    .click();
  await expect(field).toHaveAttribute("type", "text");
  await dialog
    .getByRole("button", { name: "Скрыть адрес подписки", exact: true })
    .click();
  await expect(field).toHaveAttribute("type", "password");
  await expect(field).toHaveValue(secret);
  expect(state.reveals()).toBe(0);
  const button = await dialog
    .getByRole("button", { name: "Показать адрес подписки", exact: true })
    .boundingBox();
  expect(button!.x + button!.width).toBeLessThanOrEqual(360);
});

test("revealed card expires after sixty seconds", async ({ page }) => {
  await fixture(page);
  await page.clock.install();
  const card = page.locator(".object-card").filter({ hasText: "URL fixture" });
  await card
    .getByRole("button", { name: "Показать адрес подписки", exact: true })
    .click();
  await expect(card.getByLabel("URL подписки")).toHaveValue(secret);
  await page.clock.fastForward(60_001);
  await expect(card.getByLabel("URL подписки")).toHaveValue(hint);
});

test("late reveal cannot repopulate a closed editor", async ({ page }) => {
  await fixture(page, 500);
  await page.getByRole("button", { name: "Редактировать URL fixture" }).click();
  const dialog = page.getByRole("dialog");
  await dialog
    .getByRole("button", { name: "Показать адрес подписки", exact: true })
    .click();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await page.waitForTimeout(650);
  await page.getByRole("button", { name: "Редактировать URL fixture" }).click();
  await expect(page.getByRole("dialog").getByLabel("URL подписки")).toHaveValue(
    "",
  );
});

test("failed reveal stays masked with a generic retry message", async ({
  page,
}) => {
  await fixture(page, 0, true);
  const card = page.locator(".object-card").filter({ hasText: "URL fixture" });
  await card
    .getByRole("button", { name: "Показать адрес подписки", exact: true })
    .click();
  await expect(card).toContainText(
    "Не удалось показать адрес. Повторите попытку.",
  );
  await expect(card.getByLabel("URL подписки")).toHaveValue(hint);
});
