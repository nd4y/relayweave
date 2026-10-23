import { test, expect } from "@playwright/test";
test("actual 10000-profile configuration renders bounded live worker projection", async ({
  page,
}) => {
  await page.goto("/#topology");
  await page
    .getByLabel("Пароль администратора")
    .fill(process.env.ADMIN_PASSWORD!);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  const root = page.getByTestId("traffic-topology");
  await expect(root.getByText(/каталог 10000/)).toBeVisible({ timeout: 30000 });
  const graph = root.getByRole("group", {
    name: "Интерактивная карта трафика",
  });
  await expect(graph).toBeVisible();
  expect(await graph.locator("rect").count()).toBeLessThanOrEqual(500);
  expect(
    await graph.locator("g[role=button] path").count(),
  ).toBeLessThanOrEqual(1000);
  await page.screenshot({
    path: "/reports/topology-real-catalog-10000.png",
    fullPage: true,
  });
  await root.getByLabel("Представление").click();
  await page
    .getByRole("option", { name: "Решения и события", exact: true })
    .click();
  const change = root
    .getByRole("button")
    .filter({ hasText: /configuration_observed/ })
    .first();
  await expect(change).toBeVisible();
  await change.click();
  await expect(
    page.getByText("Наблюдённое изменение", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText(/Наблюдение между кадрами/)).toBeVisible();
  await expect(
    page.getByRole("table", { name: "Альтернативы выбора" }),
  ).toHaveCount(0);
  await page.screenshot({
    path: "/reports/topology-observed-change.png",
    fullPage: true,
  });
});
