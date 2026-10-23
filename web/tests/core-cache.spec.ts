import {test,expect} from '@playwright/test';

test('inspect worker cache and remove only an unused variant through confirmation',async({page})=>{
 const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto('/');await page.getByLabel('Пароль администратора').fill(process.env.ADMIN_PASSWORD!);await page.getByRole('button',{name:'Войти',exact:true}).click();await expect(page.getByRole('heading',{name:'Обзор',exact:true})).toBeVisible();
 await page.goto('/#cores');await page.getByRole('button',{name:'Кэш ядер на воркерах',exact:true}).click();await page.getByRole('button',{name:'Обновить кэш',exact:true}).click();
 await expect(page.getByText(/Загружено \d+ из 8 сборок/)).toBeVisible();
 const current=page.locator('div.MuiBox-root').filter({has:page.getByText('Xray 26.9.30',{exact:true})}).filter({has:page.getByText('Защищена',{exact:true})}).last();await expect(current).toBeVisible();
 await page.getByRole('button',{name:'Удалить 26.9.30-compat',exact:true}).click();let dialog=page.getByRole('dialog');await expect(dialog).toContainText('worker-1');await dialog.getByRole('button',{name:'Отмена',exact:true}).click();await expect(page.getByRole('button',{name:'Удалить 26.9.30-compat',exact:true})).toBeVisible();
 await page.getByRole('button',{name:'Удалить 26.9.30-compat',exact:true}).click();dialog=page.getByRole('dialog');await dialog.getByRole('button',{name:'Удалить сборку',exact:true}).click();await expect(page.getByText(/Загружено 1 из 8 сборок/)).toBeVisible();await expect(page.getByRole('button',{name:'Удалить 26.9.30-compat',exact:true})).toHaveCount(0);
 await page.getByRole('combobox',{name:/Воркер/}).click();await page.getByRole('option',{name:'worker-2',exact:true}).click();await page.getByRole('button',{name:'Обновить кэш',exact:true}).click();await expect(page.getByText(/Загружено 1 из 8 сборок/)).toBeVisible();
 await page.evaluate(()=>window.scrollTo(0,0));await page.screenshot({path:'/reports/ui-core-cache.png',fullPage:true});expect(errors).toEqual([]);
});
