import { test, expect } from "@playwright/test";

for (const scenario of ["desktop", "mobile", "reduced"] as const) {
  test(`login illustration transfers without remounting: ${scenario}`, async ({
    page,
  }) => {
    const reduced = scenario === "reduced";
    await page.setViewportSize({
      width: scenario === "mobile" ? 390 : 1440,
      height: 1000,
    });
    await page.emulateMedia({
      reducedMotion: reduced ? "reduce" : "no-preference",
    });
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    let authenticated = false;
    await page.route("**/api/v1/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith("/auth/session")) {
        await route.fulfill({ status: authenticated ? 200 : 401, json: {} });
      } else if (path.endsWith("/auth/login")) {
        const ok =
          route.request().postDataJSON().password === "synthetic-password";
        authenticated = ok;
        await route.fulfill({
          status: ok ? 200 : 401,
          json: ok ? {} : { error: "invalid_credentials" },
        });
      } else if (path.endsWith("/events/stream")) {
        await route.fulfill({
          contentType: "text/event-stream",
          body: ": ready\n\n",
        });
      } else if (path.endsWith("/overview")) {
        // A real data wait must not replace or reset the login illustration.
        await new Promise((resolve) => setTimeout(resolve, 450));
        await route.fulfill({
          json: {
            counts: { sources: 1, profiles: 1, pools: 1 },
            sources: { fresh: 1, errors: 0, stale: 0 },
            workers: [],
            server_time: new Date().toISOString(),
          },
        });
      } else {
        await route.fulfill({ json: { items: [] } });
      }
    });
    await page.goto("/#settings");
    const art = page.locator(".rw-ov-art");
    const layer = page.locator(".rw-auth-art-layer");
    await expect(art).toBeVisible();
    await expect(layer).toHaveAttribute("data-destination", "login");
    await expect(art).toHaveAttribute("data-static", String(reduced));
    await page.evaluate(() => {
      const el = document.querySelector(".rw-ov-art")!;
      Object.assign(window, {
        rwTestArt: el,
        rwTestAnimation: el.getAnimations({ subtree: true })[0],
      });
    });
    await expect(page.locator(".login-card")).toHaveCSS("opacity", "1");
    await page.screenshot({ path: test.info().outputPath("login.png") });

    await page.getByLabel("Пароль администратора").fill("wrong");
    await page.getByRole("button", { name: "Войти", exact: true }).click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(layer).toHaveAttribute("data-destination", "login");
    await expect(page.getByLabel("Пароль администратора")).toBeVisible();

    await page.getByLabel("Пароль администратора").fill("synthetic-password");
    await page.getByRole("button", { name: "Войти", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Обзор", exact: true }),
    ).toBeVisible();
    if (!reduced) {
      await expect(layer).toHaveAttribute("data-moving", "true");
      await page.screenshot({ path: test.info().outputPath("in-flight.png") });
    }
    await expect(layer).toHaveAttribute("data-destination", "overview");
    await expect(layer).toHaveAttribute("data-moving", "false");
    expect(
      await page.evaluate(
        () =>
          (window as any).rwTestArt === document.querySelector(".rw-ov-art"),
      ),
    ).toBe(true);
    if (!reduced) {
      expect(
        await art.evaluate((el) =>
          el
            .getAnimations({ subtree: true })
            .includes((window as any).rwTestAnimation),
        ),
      ).toBe(true);
    }
    const destination = await page
      .locator('[data-network-art-anchor="overview"]')
      .boundingBox();
    const actual = await art.boundingBox();
    expect(actual!.x).toBeCloseTo(destination!.x, 0);
    expect(actual!.y).toBeCloseTo(destination!.y, 0);
    expect(actual!.width).toBeCloseTo(destination!.width, 0);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(scenario === "mobile" ? 390 : 1440);
    if (reduced)
      expect(
        await art.evaluate((el) => el.getAnimations({ subtree: true }).length),
      ).toBe(0);
    await page.screenshot({ path: test.info().outputPath("overview.png") });
    await page.getByRole("button", { name: "Выйти", exact: true }).click();
    await expect(page.getByLabel("Пароль администратора")).toBeVisible();
    await expect(layer).toHaveAttribute("data-destination", "login");
    expect(errors).toEqual([]);
  });
}
