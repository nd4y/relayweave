import {test,expect} from '@playwright/test';
test('pool preview navigates beyond first page of the actual 10k catalogue',async({page})=>{
 test.skip(!process.env.SCALE_TEST,'Requires the dedicated 10k fixture');
 await page.goto('/');await page.getByLabel('Пароль администратора').fill(process.env.ADMIN_PASSWORD!);await page.getByRole('button',{name:'Войти',exact:true}).click();await expect(page.getByRole('heading',{name:'Обзор',exact:true})).toBeVisible();
 await page.goto('/#pools');await page.getByRole('button',{name:'Предпросмотр 10,000 eligible profiles',exact:true}).click();
 const dialog=page.getByRole('dialog');await expect(dialog).toContainText('Всего: 10000');await expect(dialog.getByText('DE scale 00001',{exact:true})).toBeVisible();
 const next=page.waitForResponse(r=>r.url().includes('/preview?limit=25&offset=25'));
 await dialog.getByRole('button',{name:/следующую страницу/}).click();expect((await next).status()).toBe(200);await expect(dialog.getByText('DE scale 00026',{exact:true})).toBeVisible();await expect(dialog).toContainText('26-50 из 10000');
 await page.keyboard.press('Escape');await expect(dialog).toHaveCount(0);
});
