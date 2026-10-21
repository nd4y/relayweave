import {test,expect} from '@playwright/test';
import {writeFileSync} from 'node:fs';

test('10k catalogue interaction and loading performance',async({page})=>{
 test.skip(!process.env.SCALE_TEST,'Requires the dedicated 50-source, 10k-profile fixture');
 test.setTimeout(240000);
 await page.addInitScript(()=>{
  const state={lcp:0,events:[] as {id:number,duration:number}[]};(window as any).__performance=state;
  new PerformanceObserver(list=>{for(const entry of list.getEntries())state.lcp=entry.startTime;}).observe({type:'largest-contentful-paint',buffered:true});
  new PerformanceObserver(list=>{for(const e of list.getEntries() as any){if(e.interactionId)state.events.push({id:e.interactionId,duration:e.duration});}}).observe({type:'event',buffered:true,durationThreshold:16} as any);
 });
 await page.goto('/');await page.getByLabel('Пароль администратора').fill(process.env.ADMIN_PASSWORD!);await page.getByRole('button',{name:'Войти',exact:true}).click();
 await expect(page.getByRole('heading',{name:'Обзор',exact:true})).toBeVisible();
 const measurements:any[]=[];
 for(const width of [1440,390]){
  await page.setViewportSize({width,height:1000});
  const loads:number[]=[];
  for(let n=0;n<5;n++){
   await page.goto('/#profiles');await page.reload();
   await expect(page.getByRole('grid')).toBeVisible();
   await expect(page.getByText(/10000|10 000|10\s000/).first()).toBeVisible();
   await page.waitForTimeout(500);
   loads.push(await page.evaluate(()=>(window as any).__performance.lcp));
  }
  const search=page.getByRole('textbox',{name:'Поиск',exact:true});
  // Use actual keyboard events; fill() alone does not represent Event Timing INP.
  for(let n=0;n<8;n++){
   await search.click();await search.press('ControlOrMeta+A');await search.press('Backspace');
   await search.pressSequentially('scale '+String(n+1).padStart(2,'0'),{delay:45});
   await page.getByRole('button',{name:'Переключить тему'}).click();
  }
  await page.waitForTimeout(500);
  const observed=await page.evaluate(()=>(window as any).__performance.events as {id:number,duration:number}[]);
  const interactions=new Map<number,number>();for(const e of observed)interactions.set(e.id,Math.max(interactions.get(e.id)||0,e.duration));
  const durations=[...interactions.values()].sort((a,b)=>a-b);
  const p75=durations.length?durations[Math.ceil(durations.length*.75)-1]:16;
  const sorted=[...loads].sort((a,b)=>a-b);const lcp75=sorted[Math.ceil(sorted.length*.75)-1];
  const result={width,lcp_raw_ms:loads,lcp_p75_ms:lcp75,interaction_p75_ms:p75,observed_interaction_count:durations.length,interaction_raw_ms:durations,scope:'Chromium lab Event Timing; duration threshold 16ms; 10k actual profiles; not field Core Web Vitals'};
  measurements.push(result);console.log(JSON.stringify(result));
  await page.screenshot({path:`/reports/ui-scale-${width}.png`,fullPage:true});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(width+1);
  expect(lcp75).toBeLessThanOrEqual(2500);expect(p75).toBeLessThanOrEqual(200);
 }
 writeFileSync('/reports/ui-scale-performance.json',JSON.stringify({passed:true,measurements},null,2));
});
