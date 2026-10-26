import {test,expect} from '@playwright/test';
import net from 'node:net';

test('configure aggregate, filters, manual pool, policy and listener through the UI; backup errors and preview',async({page})=>{
 test.setTimeout(180000);const suffix=Date.now();const names={aggregate:`UI aggregate ${suffix}`,pool:`UI pool ${suffix}`,policy:`UI policy ${suffix}`,listener:`UI listener ${suffix}`};
 const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto('/');await page.getByLabel('Пароль администратора').fill(process.env.ADMIN_PASSWORD!);await page.getByRole('button',{name:'Войти',exact:true}).click();await expect(page.getByRole('heading',{name:'Обзор',exact:true})).toBeVisible();
 const add=async(kind:string,name:string)=>{await page.goto('/#'+kind);await page.getByRole('button',{name:'Добавить',exact:true}).first().click();const dialog=page.getByRole('dialog');await dialog.getByLabel(/^Название/).fill(name);return dialog;};
 const select=async(label:string,value:string)=>{await page.getByRole('dialog').getByRole('combobox',{name:label}).click();await page.getByRole('option',{name:value,exact:true}).click();};
 const save=async()=>{await page.getByRole('dialog').getByRole('button',{name:'Сохранить',exact:true}).click();await expect(page.getByRole('dialog')).toHaveCount(0);};
 const apply=async()=>{
  await page.getByRole('button',{name:/^Применить/}).first().click();const dialog=page.getByRole('dialog');await expect(dialog).toContainText('Проверка структуры пройдена');
  const response=page.waitForResponse(r=>r.url().endsWith('/api/v1/config/apply')&&r.request().method()==='POST');await dialog.getByRole('button',{name:'Применить',exact:true}).click();const op=await(await response).json();
  await expect.poll(async()=>((await(await page.request.get('/api/v1/operations/'+op.id)).json()).data.status),{timeout:60000}).toBe('succeeded');
  if(await page.getByRole('button',{name:'Закрыть операции'}).isVisible())await page.getByRole('button',{name:'Закрыть операции'}).click();
 };
 let dialog=await add('aggregates',names.aggregate);await dialog.getByRole('checkbox',{name:/^Lab primary/}).check();await save();
 dialog=await add('pools',names.pool);await select('Агрегат',names.aggregate);
 await dialog.getByRole('tab',{name:'Фильтры',exact:true}).click();await dialog.getByRole('combobox',{name:'Только страны',exact:true}).fill('DE');await page.getByRole('option',{name:'DE',exact:true}).click();
 await dialog.getByRole('tab',{name:'Выбор профилей',exact:true}).click();await dialog.getByLabel('Ручной список вместо всех профилей источников').check();await expect(dialog.getByText(/Отмечено: 1/)).toBeVisible();await save();
 const poolCard=page.locator('.object-card').filter({hasText:names.pool});await poolCard.getByRole('button',{name:'Предпросмотр '+names.pool}).click();await expect(page.getByRole('dialog')).toContainText('Всего: 1');await page.getByRole('button',{name:'Закрыть',exact:true}).click();
 dialog=await add('policies',names.policy);await select('Пул профилей',names.pool);await select('Режим распределения','Максимальная скорость');await dialog.getByLabel('Активных путей').fill('1');await dialog.getByLabel('Готовых резервных').fill('0');await dialog.getByLabel('URL проверки доступности').fill('http://origin:9000/healthz');await save();
 dialog=await add('listeners',names.listener);await select('Протокол','HTTP / CONNECT');await dialog.getByLabel('Адрес внутри контейнера').fill('0.0.0.0:8082');await select('Политика',names.policy);await dialog.getByLabel('Точка входа включена').uncheck();await dialog.getByLabel('Имя пользователя').fill('ui-client');await dialog.getByLabel('Пароль',{exact:true}).fill('ui-fixture-password');
 await dialog.getByLabel('Разрешённые сети (CIDR)').fill('invalid-cidr');const rejected=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith('/api/v1/listeners'));await dialog.getByRole('button',{name:'Сохранить',exact:true}).click();expect((await rejected).status()).toBe(400);await expect(dialog).toBeVisible();await dialog.getByLabel('Разрешённые сети (CIDR)').fill('127.0.0.0/8');await save();
 await page.getByRole('button',{name:'Редактировать HTTP primary',exact:true}).click();await select('Политика',names.policy);await save();await apply();
 const response=await new Promise<string>((resolve,reject)=>{const socket=net.createConnection({host:'127.0.0.1',port:Number(process.env.HTTP_PORT||48080)});let text='';socket.setTimeout(10000,()=>socket.destroy(new Error('proxy response timeout')));socket.on('connect',()=>socket.write('GET http://origin:9000/healthz HTTP/1.1\r\nHost: origin\r\nConnection: close\r\n\r\n'));socket.on('data',chunk=>text+=chunk);socket.on('error',reject);socket.on('end',()=>resolve(text));});expect(response).toContain('200 OK');expect(response).toContain('ok');
 await page.goto('/#listeners');await page.getByRole('button',{name:'Редактировать HTTP primary',exact:true}).click();await select('Политика','Primary policy');await save();await apply();
 for(const [kind,name] of [['listeners',names.listener],['policies',names.policy],['pools',names.pool],['aggregates',names.aggregate]]){
  await page.goto('/#'+kind);const card=page.locator('.object-card').filter({hasText:name});await card.getByRole('button',{name:'Удалить '+name,exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'Подтвердить',exact:true}).click();await expect(card).toHaveCount(0);
 }
 await page.goto('/#settings');await page.getByRole('tab',{name:'Резервные копии',exact:true}).click();await page.getByLabel('Пароль резервной копии').fill('ui-backup-fixture-2026');
 const downloadEvent=page.waitForEvent('download');await page.getByRole('button',{name:'Скачать копию',exact:true}).click();const download=await downloadEvent;const file=await download.path();expect(file).toBeTruthy();
 await page.locator('input[type=file]').setInputFiles({name:'damaged.rwb',mimeType:'application/octet-stream',buffer:Buffer.from('damaged')});const invalid=page.waitForResponse(r=>r.url().endsWith('/api/v1/backup/preview'));await page.getByRole('button',{name:'Проверить копию',exact:true}).click();expect((await invalid).status()).toBe(400);await expect(page.getByText('Неверный пароль резервной копии или повреждённый файл.')).toBeVisible();
 await page.locator('input[type=file]').setInputFiles(file!);await page.getByRole('button',{name:'Проверить копию',exact:true}).click();await expect(page.getByText(/Копия проверена/)).toBeVisible();
 await page.screenshot({path:'/reports/ui-backup-verified.png',fullPage:true});expect(errors).toEqual([]);
});
