import {test,expect} from '@playwright/test';

test('Grafana shares the administrator session and renders real metrics',async({page})=>{
  const request=page.request;
  const unauth=await request.get('/grafana/api/search',{maxRedirects:0,headers:{'X-WEBAUTH-USER':'admin'}});
  expect(unauth.status()).toBe(302);expect(unauth.headers().location).toBe('/');
  const login=await request.post('/api/v1/auth/login',{data:{password:process.env.ADMIN_PASSWORD}});
  expect(login.ok()).toBeTruthy();const session=await login.json();
  const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto('/grafana/d/rw-overview');
  await expect(page.getByText('Active TCP connections',{exact:true})).toBeVisible({timeout:30000});
  await expect(page.getByText('Live payload traffic, excludes handshake',{exact:true})).toBeVisible();
  await page.screenshot({path:'/reports/ui-grafana.png',fullPage:true});
  expect(errors).toEqual([]);
  const logout=await request.post('/api/v1/auth/logout',{data:{},headers:{'X-CSRF-Token':session.csrf}});expect(logout.ok()).toBeTruthy();
  const after=await request.get('/grafana/api/search',{maxRedirects:0});expect(after.status()).toBe(302);
});
