import {chromium, expect} from '@playwright/test';

// Run only while the labelled observability fixture is actually stopped.
const browser = await chromium.launch({headless: true});
try {
  for (const [name, width, height, colorScheme] of [
    ['desktop', 1280, 900, 'dark'], ['mobile', 360, 800, 'light'],
  ]) {
    const context = await browser.newContext({viewport: {width, height}, colorScheme, reducedMotion: 'reduce'});
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const base = process.env.BASE_URL || 'http://127.0.0.1:18443';
    const login = await page.request.post(base + '/api/v1/auth/login', {data: {password: process.env.ADMIN_PASSWORD}});
    expect(login.ok()).toBeTruthy();
    const response = await page.goto(base + '/grafana/');
    expect(response.status()).toBe(503);
    expect(response.headers()['retry-after']).toBe('5');
    await expect(page.getByRole('heading', {name: 'Мониторинг временно недоступен'})).toBeVisible();
    await expect(page.getByRole('status')).toContainText('Прокси работают независимо');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
    await page.getByRole('link', {name: 'Повторить сейчас'}).focus();
    await expect(page.getByRole('link', {name: 'Повторить сейчас'})).toBeFocused();
    await page.screenshot({path: `/reports/ui-grafana-unavailable-${name}.png`, fullPage: true});
    const retry = page.waitForResponse(r => r.request().isNavigationRequest());
    await page.getByRole('link', {name: 'Повторить сейчас'}).click();
    expect((await retry).status()).toBe(503);
    await page.getByRole('link', {name: 'В приложение'}).click();
    await expect(page).toHaveURL(base + '/');
    await expect(page.getByRole('heading', {name: 'Обзор', exact: true})).toBeVisible();
    expect(errors).toEqual([]);
    await context.close();
  }
  console.log('Actual Grafana outage UI passed on desktop/mobile, including retry, focus and return.');
} finally {
  await browser.close();
}
