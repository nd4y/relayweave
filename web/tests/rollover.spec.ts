import { test, expect } from "@playwright/test";

test("routine address rotation explains automatic stages without Accept", async ({ page }) => {
  let state = "warming";
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  const source = { id: "source1", kind: "sources", name: "Synthetic provider", version: 7, updated_at: new Date().toISOString(), data: { enabled: true, profile_count: 2, last_fetched: new Date().toISOString(), last_success: new Date().toISOString(), max_stale_seconds: 3600, core_version: "26.10.17", url_hint: "DO_NOT_DISPLAY_SOURCE_URL", update_policy: "auto_safe" } };
  await page.route("**/api/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/v1/", "");
    if (path === "events/stream") { await route.fulfill({ status: 200, contentType: "text/event-stream", body: ": synthetic\n\n" }); return; }
    let body: unknown = { items: [], total: 0 };
    if (path === "auth/session") body = { authenticated: true, csrf: "synthetic" };
    if (path === "sources") body = { items: [source], total: 1 };
    if (path === "overview") body = { counts: { sources: 1, profiles: 2, pools: 1, policies: 1, listeners: 1 }, sources: { fresh: 1, stale: 0, errors: 0 }, events: [], settings: {}, workers: [{ id: "worker-1", online: true, seen_at: new Date().toISOString(), generation: 2, status: { active_connections: 2, rollovers: [{ source_id: "source1", route: "r", state, reason: "synthetic", new_admission_percent: 20, old_active: 2 }] } }] };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
  for (const [next, label] of [
    ["warming", "Проверяем новые адреса"],
    ["canary", "Плавно переключаем соединения"],
    ["active_draining", "Новые адреса активны, старые сессии завершаются"],
    ["paused", "Обновление отложено, повторим автоматически"],
  ]) {
    state = next;
    await page.goto("/#sources"); await page.reload();
    const card = page.locator(".object-card").filter({ hasText: "Synthetic provider" });
    await expect(card).toContainText(label);
    await expect(card).toContainText("Автоматический выбор профилей и весов");
    const status = card.locator(".MuiChip-label").filter({ hasText: label });
    expect(await status.evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
    const profileCount = card.getByText("2 профиля", { exact: true });
    await expect(profileCount).toBeVisible();
    const bounds = await card.boundingBox(); const countBounds = await profileCount.boundingBox();
    expect(countBounds!.x + countBounds!.width).toBeLessThanOrEqual(bounds!.x + bounds!.width);
    await expect(card.getByRole("button", { name: "Принять", exact: true })).toHaveCount(0);
    await expect(page.getByText("DO_NOT_DISPLAY_SOURCE_URL")).toHaveCount(0);
  }
  expect(errors).toEqual([]);
  await page.screenshot({ path: "/reports/rollover-automatic.png", fullPage: true });
});
