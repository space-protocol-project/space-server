import assert from 'node:assert/strict';
import {chromium} from 'playwright';
import {mkdtemp,rm} from 'node:fs/promises';
import {join} from 'node:path';
import {tmpdir} from 'node:os';
if(process.env.CI!=='true')throw new Error('Только одноразовый CI');
const origin='http://127.0.0.1:18080',profile=await mkdtemp(join(tmpdir(),'space-rotation-ui-'));
let context;
async function open(){
 context=await chromium.launchPersistentContext(profile,{headless:true});
 const page=await context.newPage();page.on('dialog',d=>d.accept());
 await page.goto(origin+'/space');await page.waitForFunction(()=>!document.querySelector('#sign-in').disabled);return page;
}
async function record(page){return page.evaluate(async()=>{
 const db=await new Promise((resolve,reject)=>{const open=indexedDB.open('space-panel-identity-v1',1);open.onsuccess=()=>resolve(open.result);open.onerror=()=>reject(open.error);});
 try{return await new Promise((resolve,reject)=>{const tx=db.transaction('keys','readonly'),store=tx.objectStore('keys');const active=store.get(location.origin),pending=store.get(location.origin+':rotation');tx.oncomplete=()=>resolve({grantId:active.result?.grantId,history:active.result?.rootHistory?.length||0,pending:!!pending.result});tx.onerror=()=>reject(tx.error);});}finally{db.close();}
});}
try{
 let page=await open();await page.locator('#sign-in').click();await page.locator('#participant').waitFor({state:'visible'});
 const original=await record(page);
 await page.route('**/api/v1/auth/root-rotations/complete',async route=>{await route.fetch();await route.abort();});
 await page.locator('#rotate-root').click();
 await page.waitForFunction(()=>document.querySelector('#status').textContent.includes('fetch'));
 assert.equal((await record(page)).pending,true);assert.equal((await record(page)).grantId,original.grantId);
 await context.close();context=undefined;
 page=await open();await page.locator('#finish-root-rotation').click();await page.locator('#participant').waitFor({state:'visible'});
 const rotated=await record(page);assert.equal(rotated.history,1);assert.equal(rotated.pending,false);assert.notEqual(rotated.grantId,original.grantId);
 await context.close();context=undefined;
 page=await open();await page.locator('#sign-in').click();await page.locator('#participant').waitFor({state:'visible'});
 assert.deepEqual(await record(page),rotated);
 console.log('/space: ротация, потерянный ответ после commit, закрытие браузера, resume и повторный вход — успешно.');
}finally{await context?.close();await rm(profile,{recursive:true,force:true});}
