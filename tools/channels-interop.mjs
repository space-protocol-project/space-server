import assert from 'node:assert/strict';
import { chromium } from 'playwright';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
import { request, proof, newKeys } from '../internal/spaceweb/assets/identity.mjs';
import { createRecoveryCard } from '../internal/spaceweb/assets/recovery.mjs';

async function nextLine(lines) {
  let timer;
  try {return await Promise.race([lines.next(),new Promise((_,reject)=>{timer=setTimeout(()=>reject(new Error('Flutter не ответил за 45 секунд')),45000);})]);}
  finally {clearTimeout(timer);}
}

// Только одноразовые ключи CI, отдельный профиль Chromium, без пользовательского vault.
export async function channelsInterop(origin,discovery,keys,session) {
  if(process.env.CI!=='true')throw new Error('Только изолированный CI');
  const api=(path,body,method)=>request(origin,path,body,session.accessToken,method);
  let context,child;
  const password='Disposable channel UI card 2026';
  try {
    const card=await createRecoveryCard(keys,origin,discovery,password,authority=>proof(origin,discovery,authority,'device.register','','',{recovery:true}));
    context=await chromium.launch({headless:true});
    const page=await context.newPage({viewport:{width:390,height:844}});
    page.setDefaultTimeout(20000);
    page.on('dialog',dialog=>dialog.accept());
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto(origin+'/space');
    await page.getByText('Восстановить прежнюю идентичность',{exact:true}).click();
    await page.locator('#restore-file').setInputFiles({name:'card.json',mimeType:'application/json',buffer:Buffer.from(card)});
    await page.locator('#restore-password').fill(password);
    await page.locator('#restore-form button').click();
    await page.locator('#management').waitFor({state:'visible'});
    await page.waitForFunction(()=>!document.querySelector('#reload-channels').disabled);
    await page.getByText('Создать чат-канал',{exact:true}).click();
    await page.locator('#new-channel-id').fill('ui-workshop');
    await page.locator('#new-channel-title').fill('Рабочая группа');
    await page.locator('#new-channel-position').fill('100');
    await page.locator('#create-channel button').click();
    await page.waitForFunction(()=>document.querySelector('#status').textContent.includes('Канал создан'));
    await page.locator('#channel-title').fill('Обновлённая группа');
    await page.locator('#channel-form button').click();
    await page.waitForFunction(()=>document.querySelector('#status').textContent.includes('Настройки канала сохранены'));
    // Удаляем member/reader: закрытая группа только для администрации.
    while(await page.locator('#channel-rules fieldset').count())await page.locator('#channel-rules fieldset button').first().click();
    await page.locator('#save-channel-access').click();
    await page.waitForFunction(()=>document.querySelector('#status').textContent.includes('Права канала сохранены'));
    const acl=await api('/api/v1/channels/ui-workshop/access');assert.equal((acl.rules||[]).length,0);
    // Внешнее изменение должно вызвать конфликт и сохранить несохранённое поле.
    let c=(await api('/api/v1/channels?includeArchived=true')).channels.find(c=>c.id==='ui-workshop');
    await api('/api/v1/channels/ui-workshop',{title:c.title,position:c.position,archived:false,publicPreview:false,expectedRevision:c.revision},'PATCH');
    await page.locator('#channel-title').fill('Несохранённый текст');await page.locator('#channel-form button').click();
    await page.waitForFunction(()=>document.querySelector('#status').textContent.includes('Канал уже изменён'));
    assert.equal(await page.locator('#channel-title').inputValue(),'Несохранённый текст');
    await page.locator('#reload-channel-access').click();
    await page.waitForFunction(()=>document.querySelector('#status').textContent.includes('загружены заново'));
    await page.locator('#channel-archived').check();await page.locator('#channel-form button').click();
    await page.waitForFunction(()=>document.querySelector('#status').textContent.includes('Настройки канала сохранены'));
    assert.ok(await page.locator('[data-channel-id="ui-workshop"]').textContent().then(t=>t.includes('Архив')));
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
    assert.deepEqual(errors,[]);
    if(process.env.SPACE_CHANNELS_SCREENSHOT) {await page.setViewportSize({width:1100,height:900});await page.locator("#channels-panel").screenshot({path:process.env.SPACE_CHANNELS_SCREENSHOT});}
    // Проверяем доступный список при общем выключении чатов и возвращаем настройку.
    await page.locator('#chat-enabled').uncheck();await page.locator('#settings-form button[type="submit"], #settings-form button:not([type])').click();
    await page.waitForFunction(()=>document.querySelector('#status').textContent.includes('Настройки сохранены на сервере'));
    assert.ok(await page.locator('[data-channel-id="ui-workshop"]').count());
    await page.locator('#chat-enabled').check();await page.locator('#settings-form button:not([type])').click();
    await page.waitForFunction(()=>document.querySelector('#status').textContent.includes('Настройки сохранены на сервере'));
    await page.goto(origin+'/space/flutter/');
    await page.getByRole('button',{name:'Войти с сохранёнными ключами',exact:true}).click();
    await page.getByRole('textbox',{name:/Название пространства/}).waitFor();
    const oldTitle=(await api('/api/v1/space/settings')).settings.title;
    await page.getByRole('textbox',{name:/Название пространства/}).fill('Настройка из общего Flutter-модуля');
    const saveSettingsReply=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/space/settings'&&r.request().method()==='PATCH');
    await page.getByRole('button',{name:'Сохранить настройки',exact:true}).click();
    assert.equal((await saveSettingsReply).status(),200);
    assert.equal((await api('/api/v1/space/settings')).settings.title,'Настройка из общего Flutter-модуля');
    if(process.env.SPACE_ADMIN_FLUTTER_SCREENSHOT)await page.screenshot({path:process.env.SPACE_ADMIN_FLUTTER_SCREENSHOT,fullPage:true});
    await page.getByRole('textbox',{name:/Название пространства/}).fill(oldTitle);
    const restoreSettingsReply=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/space/settings'&&r.request().method()==='PATCH');
    await page.getByRole('button',{name:'Сохранить настройки',exact:true}).click();
    assert.equal((await restoreSettingsReply).status(),200);
    assert.equal((await api('/api/v1/space/settings')).settings.title,oldTitle);
    // Каналы того же Flutter-модуля: создание, смена прав и архив.
    async function reach(text,role='button') {
      const target=role?page.getByRole(role,{name:text,exact:true}):page.getByText(text,{exact:true});
      for(let i=0;i<60;i++) {
        if(await target.count()) {try {await target.first().scrollIntoViewIfNeeded({timeout:1500});return target;}catch {}}
        await page.mouse.move(550,650);await page.mouse.wheel(0,i===0?-10000:500);await page.waitForTimeout(150);
      }
      console.log('Диагностика Flutter:',(await page.locator('flt-semantics').allTextContents()).join(' | ').slice(-12000));
      if(process.env.SPACE_ADMIN_FLUTTER_SCREENSHOT)await page.screenshot({path:process.env.SPACE_ADMIN_FLUTTER_SCREENSHOT});
      throw new Error('Не найден элемент Flutter: '+text);
    }
    await (await reach('Создать чат-канал')).click();
    await page.getByRole('textbox',{name:/Адрес нового канала/}).fill('flutter-team');
    await page.getByRole('textbox',{name:/Название нового канала/}).fill('Группа Flutter');
    const createdReply=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/channels'&&r.request().method()==='POST');
    await (await reach('Создать канал')).click();
    assert.equal((await createdReply).status(),200);
    const createdFlutter=(await api('/api/v1/channels?includeArchived=true')).channels.find(c=>c.id==='flutter-team');
    assert.ok(createdFlutter);
    for(let i=0;i<2;i++)await (await reach('Удалить правило')).first().click();
    const accessReply=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/channels/flutter-team/access'&&r.request().method()==='PUT');
    await (await reach('Сохранить права')).click();
    assert.equal((await accessReply).status(),200);
    assert.equal(((await api('/api/v1/channels/flutter-team/access')).rules||[]).length,0);
    await (await reach('Архивировать канал','switch')).click();
    const archivedReply=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/channels/flutter-team'&&r.request().method()==='PATCH');
    await (await reach('Сохранить канал')).click();
    assert.equal((await archivedReply).status(),200);
    assert.equal((await api('/api/v1/channels?includeArchived=true')).channels.find(c=>c.id==='flutter-team').archived,true);
    if(process.env.SPACE_ADMIN_FLUTTER_SCREENSHOT)await page.screenshot({path:process.env.SPACE_ADMIN_FLUTTER_SCREENSHOT});
    // Общий модуль приглашений: выпуск и отзыв с подтверждением.
    const inviteReply=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/space/invites'&&r.request().method()==='POST');
    await (await reach('Создать приглашение')).click();
    const inviteResult=await inviteReply;assert.equal(inviteResult.status(),200);
    const issued=await inviteResult.json();
    await (await reach('Скрыть код')).click();
    assert.equal(await page.getByText(issued.token,{exact:true}).count(),0);
    const available=await api('/api/v1/space/invites');
    assert.ok(available.invites.every(i=>!Object.hasOwn(i,'token')));
    const revokeReply=page.waitForResponse(r=>new URL(r.url()).pathname===`/api/v1/space/invites/${issued.invite.id}/revoke`&&r.request().method()==='POST');
    const loadedInvites=(await api('/api/v1/space/invites')).invites;
    const activeInvites=loadedInvites.filter(i=>!i.revoked&&Number(i.expiresAt)>Date.now()/1000&&Number(i.uses||0)<i.maxUses);
    const issuedIndex=activeInvites.findIndex(i=>i.id===issued.invite.id);
    assert.ok(issuedIndex>=0);
    await (await reach('Отозвать приглашение')).nth(issuedIndex).scrollIntoViewIfNeeded();
    await page.getByRole('button',{name:'Отозвать приглашение',exact:true}).nth(issuedIndex).click();
    await page.getByRole('button',{name:'Отозвать приглашение',exact:true}).last().click();
    assert.equal((await revokeReply).status(),200);
    assert.equal((await api('/api/v1/space/invites')).invites.find(i=>i.id===issued.invite.id).revoked,true);
    const guestKeys=await newKeys();
    const guestGrant=await proof(origin,discovery,guestKeys,'device.register','');guestKeys.grantId=guestGrant.grantId;
    const guestSession=await proof(origin,discovery,guestKeys,'auth.login',guestKeys.grantId);
    const memberListReply=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/space/members'&&r.request().method()==='GET');
    await (await reach('Обновить участников')).click();await memberListReply;
    await (await reach('Поиск по идентификатору или роли','textbox')).fill(guestGrant.principalId);
    await (await reach('Изменить права')).click();
    await (await reach('Заблокировать участника','switch')).click();
    const memberReply=page.waitForResponse(r=>new URL(r.url()).pathname===`/api/v1/space/members/${guestGrant.principalId}`&&r.request().method()==='PATCH');
    await (await reach('Сохранить права участника')).click();
    await page.getByRole('button',{name:'Применить права',exact:true}).click();
    assert.equal((await memberReply).status(),200);
    assert.equal((await api('/api/v1/space/members')).members.find(m=>m.principalId===guestGrant.principalId).blocked,true);
    await assert.rejects(request(origin,'/api/v1/channels/general/content',undefined,guestSession.accessToken),e=>e.status===403);
    await (await reach('Заблокировать участника','switch')).click();
    const restoredMemberReply=page.waitForResponse(r=>new URL(r.url()).pathname===`/api/v1/space/members/${guestGrant.principalId}`&&r.request().method()==='PATCH');
    await (await reach('Сохранить права участника')).click();await page.getByRole('button',{name:'Применить права',exact:true}).click();
    assert.equal((await restoredMemberReply).status(),200);
    if(process.env.SPACE_ADMIN_FLUTTER_SCREENSHOT)await page.screenshot({path:process.env.SPACE_ADMIN_FLUTTER_SCREENSHOT});
    assert.deepEqual(errors,[]);
    await context.close();context=undefined;

    // До входа general не раскрывается, но Flutter всё равно входит по своему ключу.
    const general=(await api('/api/v1/channels')).channels.find(c=>c.id==='general');
    await api('/api/v1/channels/general',{title:general.title,position:general.position,archived:false,publicPreview:false,expectedRevision:general.revision},'PATCH');
    await api('/api/v1/channels',{channelId:'interop-private',title:'Закрытый тестовый канал',viewType:'chat',position:100});
    child=spawn('dart',['run','tool/channels_interoperability.dart',origin],{cwd:fileURLToPath(new URL('../../client/',import.meta.url)),stdio:['pipe','pipe','pipe']});
    const exited=new Promise((resolve,reject)=>{child.once('error',reject);child.once('exit',(code)=>resolve(code));});
    let stderr='';child.stderr.on('data',data=>stderr+=data);
    const lines=createInterface({input:child.stdout})[Symbol.asyncIterator]();
    const first=await nextLine(lines);assert.ok(!first.done);const {principal}=JSON.parse(first.value);
    const privateChannel=(await api('/api/v1/channels')).channels.find(c=>c.id==='interop-private');
    await api('/api/v1/channels/interop-private/access',{expectedRevision:privateChannel.revision,rules:[{principalId:principal,permissions:{visible:true,read:true,write:true,manage:false}}]},'PUT');
    child.stdin.end('continue\n');
    const second=await nextLine(lines);assert.ok(!second.done);assert.equal(JSON.parse(second.value).ok,true);
    assert.equal(await exited,0,stderr);child=undefined;
    console.log('Каналы: формы /space, ACL, архив, конфликт revision, узкий экран и Flutter вход/изолированный чат — успешно.');
  } finally {
    await context?.close();child?.kill();
    // Настройки одноразового CI-сервера нужны оставшимся interoperability проверкам.
    const all=await api('/api/v1/channels?includeArchived=true');
    const general=all.channels.find(c=>c.id==='general');
    if(general&&!general.publicPreview)await api('/api/v1/channels/general',{title:general.title,position:general.position,archived:false,publicPreview:true,expectedRevision:general.revision},'PATCH');
  }
}
