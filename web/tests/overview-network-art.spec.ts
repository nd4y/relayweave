import { test, expect } from "@playwright/test";

test("overview art respects motion preferences, visibility and navigation", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/events/stream")) {
      await route.fulfill({
        contentType: "text/event-stream",
        body: ": ready\n\n",
      });
      return;
    }
    await route.fulfill({
      json: path.endsWith("/overview")
        ? {
            counts: { sources: 1, profiles: 1, pools: 1 },
            sources: { fresh: 1, errors: 0, stale: 0 },
            workers: [],
            server_time: new Date().toISOString(),
          }
        : { items: [] },
    });
  });
  await page.goto("/#overview");
  const art = page.locator(".rw-ov-art");
  await expect(art).toHaveAttribute("data-paused", "false");
  await expect(art).toHaveAttribute("aria-hidden", "true");
  const geometry = await art.boundingBox();
  await expect
    .poll(() =>
      art.evaluate((el) => el.getAnimations({ subtree: true }).length),
    )
    .toBe(15);
  await page.waitForTimeout(250);
  expect(await art.boundingBox()).toEqual(geometry);
  await page.screenshot({ path: test.info().outputPath("overview-dark.png") });
  await page.getByRole("button", { name: "Переключить тему" }).click();
  await page.screenshot({ path: test.info().outputPath("overview-light.png") });

  // Responsive hiding must stop all animations, including pseudo-element replies.
  await page.setViewportSize({ width: 768, height: 900 });
  await expect(art).toBeHidden();
  await expect(art).toHaveAttribute("data-paused", "true");
  await expect
    .poll(() =>
      art.evaluate((el) =>
        el
          .getAnimations({ subtree: true })
          .every((a) => a.playState === "paused"),
      ),
    )
    .toBe(true);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await expect(art).toHaveAttribute("data-paused", "false");

  // Move the hero outside the viewport to exercise the real IntersectionObserver.
  await page.locator(".hero-panel").evaluate((el) => {
    (el as HTMLElement).style.marginTop = "200vh";
  });
  await expect(art).toHaveAttribute("data-paused", "true");
  await page.locator(".hero-panel").evaluate((el) => {
    (el as HTMLElement).style.marginTop = "";
  });
  await art.scrollIntoViewIfNeeded();
  await expect(art).toHaveAttribute("data-paused", "false");

  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(art).toHaveAttribute("data-static", "true");
  await expect
    .poll(() =>
      art.evaluate((el) => el.getAnimations({ subtree: true }).length),
    )
    .toBe(0);
  await page.screenshot({
    path: test.info().outputPath("overview-reduced.png"),
  });
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await expect(art).toHaveAttribute("data-paused", "false");

  // Navigation unmounts and remounts the component, without stale observers.
  await page.goto("/#sources");
  await expect(art).toHaveCount(0);
  await page.goto("/#overview");
  await expect(art).toHaveAttribute("data-paused", "false");
  expect(errors).toEqual([]);
});
