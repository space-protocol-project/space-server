import assert from 'node:assert/strict';
import {chromium} from 'playwright';
import {mkdtemp,writeFile,rm} from 'node:fs/promises';
import {join} from 'node:path';
import {tmpdir} from 'node:os';
import {newKeys,proof,request} from '../internal/spaceweb/assets/identity.mjs';
import {createRecoveryCard} from '../internal/spaceweb/assets/recovery.mjs';
import {qrRaster} from '../internal/spaceweb/assets/recovery-qr.mjs';
if(process.env.CI!=='true')throw new Error('Только изолированный CI с синтетической камерой');
const origin='http://127.0.0.1:18080',directory=await mkdtemp(join(tmpdir(),'space-camera-'));
let browser;
try{
 const discovery=await(await fetch(origin+'/.well-known/space-protocol')).json(),keys=await newKeys();
 const grant=await proof(origin,discovery,keys,'device.register','');keys.grantId=grant.grantId;
 const password='Disposable camera recovery password';
 const packet=await createRecoveryCard(keys,origin,discovery,password,k=>proof(origin,discovery,k,'device.register','','',{recovery:true}));
 const raster=qrRaster(packet),w=raster.width,h=raster.height;
 const y=Buffer.alloc(w*h),uv=Buffer.alloc(w*h/4,128);
 for(let i=0;i<y.length;i++)y[i]=raster.data[i*4]===0?16:235;
 const path=join(directory,'qr.y4m');
 await writeFile(path,Buffer.concat([Buffer.from(`YUV4MPEG2 W${w} H${h} F30:1 Ip A1:1 C420jpeg\n`),...Array.from({length:10},()=>[Buffer.from('FRAME\n'),y,uv,uv]).flat()]));
 browser=await chromium.launch({channel:'chromium',headless:true,args:['--use-fake-ui-for-media-stream','--use-fake-device-for-media-stream',`--use-file-for-fake-video-capture=${path}`]});
 const context=await browser.newContext({permissions:['camera']});
 const page=await context.newPage();
 page.on('dialog',d=>d.accept());
 await page.goto(origin+'/space');
 await page.waitForFunction(()=>!document.querySelector('#sign-in').disabled);
 await page.getByText('Восстановить прежнюю идентичность',{exact:true}).click();
 // Обёртка только тестовой камеры: проверяет отсутствие audio и остановку track.
 await page.evaluate(()=>{
   window.cameraTracks=[];window.cameraErrors=[];
   const capture=navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
   navigator.mediaDevices.getUserMedia=async c=>{
     if(c.audio!==false)throw new Error('Микрофон не должен запрашиваться');
     try {const stream=await capture(c);window.cameraTracks.push(...stream.getTracks());return stream;} catch(error){window.cameraErrors.push({name:error.name,message:error.message});throw error;}
   };
 });
 await page.locator('#scan-card-camera').click();
 await page.waitForFunction(()=>document.querySelector('#camera-card-ready').textContent.includes('Карточка прочитана'),{},{timeout:20000});
 assert.equal(await page.locator('#camera-dialog').evaluate(d=>d.open),false);
 assert.equal(await page.evaluate(()=>window.cameraTracks.every(t=>t.readyState==='ended')),true);
 await page.locator('#restore-password').fill(password);
 await page.locator('#restore-form button').click();
 await page.locator('#participant').waitFor({state:'visible'});
 // Новый захват отменяется пользователем; track также обязан закрыться.
 await page.evaluate(()=>document.querySelector('#participant-sign-out').click());
 await page.locator('#scan-card-camera').click();
 await page.waitForFunction(()=>window.cameraTracks.length>=2);
 if(await page.locator('#camera-dialog').evaluate(d=>d.open))await page.locator('#camera-dialog button').click();
 assert.equal(await page.evaluate(()=>window.cameraTracks.every(t=>t.readyState==='ended')),true);
 await context.close();
 console.log('Браузер: синтетическая камера → QR → пароль → реальное восстановление; audio=false и остановка tracks — успешно.');
}catch(error){
 if(browser){const contexts=browser.contexts();const page=contexts[0]?.pages()[0];if(page)console.log(await page.evaluate(()=>({status:document.querySelector('#camera-dialog [role=status]')?.textContent,video:[document.querySelector('video')?.videoWidth,document.querySelector('video')?.videoHeight],errors:window.cameraErrors,secure:isSecureContext,media:!!navigator.mediaDevices,tracks:window.cameraTracks?.map(t=>({kind:t.kind,state:t.readyState}))})));}
 throw error;
}finally{await browser?.close();await rm(directory,{recursive:true,force:true});}
