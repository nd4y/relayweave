import { chromium, expect } from '@playwright/test';

const browser = await chromium.launch();
try {
  const page = await browser.newPage();
  const errors = [];
  const keys = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => {
    if (['auth/login', 'auth/logout'].some(path => request.url().endsWith('/api/v1/' + path))) {
      keys.push(request.headers()['idempotency-key']);
    }
  });
  await page.goto(process.env.BASE_URL);
  const capabilities = await page.evaluate(() => ({
    secure: isSecureContext,
    randomUUID: typeof crypto.randomUUID,
    getRandomValues: typeof crypto.getRandomValues,
  }));
  expect(capabilities).toEqual({ secure: false, randomUUID: 'undefined', getRandomValues: 'function' });
  await page.getByLabel('Пароль администратора').fill(process.env.ADMIN_PASSWORD);
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Обзор', exact: true })).toBeVisible({ timeout: 20000 });
  if (process.env.EXPECTED_GRAFANA_URL) {
    await expect(page.getByRole('link', { name: 'Открыть Grafana', exact: true })).toHaveAttribute('href', process.env.EXPECTED_GRAFANA_URL);
    for (const path of ['/grafana', '/grafana/']) {
      const response = await page.request.get(new URL(path, process.env.BASE_URL).href);
      expect(response.status()).toBe(404);
    }
  }
  await page.getByRole('button', { name: 'Выйти', exact: true }).click();
  await expect(page.getByLabel('Пароль администратора')).toBeVisible();
  expect(keys).toHaveLength(2);
  for (const key of keys) expect(key).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  expect(new Set(keys).size).toBe(2);
  expect(errors).toEqual([]);
  console.log(JSON.stringify({ passed: true, capabilities, login: true, logout: true, distinct_valid_idempotency_keys: 2, browser_errors: 0 }));
} finally {
  await browser.close();
}
