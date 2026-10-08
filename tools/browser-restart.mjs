import assert from 'node:assert/strict';
import { chromium } from 'playwright';
import { mkdtemp, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

// Только одноразовый сервер GitHub CI и отдельные временные профили.
const origin = 'http://127.0.0.1:18080';
if (process.env.CI !== 'true') throw new Error('Этот тест предназначен только для изолированного CI.');
const directory = await mkdtemp(join(tmpdir(), 'space-browser-restart-'));
let context;
async function open(profile) {
  context = await chromium.launchPersistentContext(join(directory, profile), {headless:true});
  context.setDefaultTimeout(15000);
  const page = await context.newPage();
  page.on('dialog', dialog => dialog.accept());
  await page.goto(origin + '/space');
  await page.locator('#sign-in').waitFor({state:'visible'});
  await page.waitForFunction(() => !document.querySelector('#sign-in').disabled);
  return page;
}
async function stored(page) {
  return page.evaluate(async () => {
    const db = await new Promise((resolve,reject) => {
      const open = indexedDB.open('space-panel-identity-v1',1);
      open.onupgradeneeded = () => open.result.createObjectStore('keys');
      open.onsuccess = () => resolve(open.result);
      open.onerror = () => reject(open.error);
    });
    try {
      const value = await new Promise((resolve,reject) => {
        const tx = db.transaction('keys','readonly');
        const get = tx.objectStore('keys').get(location.origin);
        get.onsuccess = () => resolve(get.result);
        get.onerror = () => reject(get.error);
      });
      if (!value) return null;
      const root = Array.from(new Uint8Array(await crypto.subtle.exportKey('raw',value.root.publicKey)));
      return {root,grantId:value.grantId,hasRecovery:!!value.recoveryPrivate,
        nonExtractable:value.device.privateKey.extractable === false};
    } finally {db.close();}
  });
}
try {
  let page = await open('original');
  await page.locator('#sign-in').click();
  await page.locator('#participant').waitFor({state:'visible'});
  const first = await stored(page);
  assert.ok(first.grantId && first.nonExtractable);
  await page.locator('#card-password').fill('Disposable CI card password 2026');
  await page.locator('#card-confirm').fill('Disposable CI card password 2026');
  const jsonDownload = page.waitForEvent('download');
  await page.locator('#card-form button').click();
  await (await jsonDownload).saveAs(join(directory,'card.json'));
  const pngDownload = page.waitForEvent('download');
  await page.locator('#download-card-png').click();
  await (await pngDownload).saveAs(join(directory,'card.png'));
  await context.close(); context = undefined;

  page = await open('original');
  assert.deepEqual(await stored(page), first);
  await page.locator('#sign-in').click();
  await page.locator('#participant').waitFor({state:'visible'});
  assert.deepEqual(await stored(page), first);
  await context.close(); context = undefined;

  page = await open('clean');
  assert.equal(await stored(page), null);
  await page.getByText('Восстановить прежнюю идентичность',{exact:true}).click();
  await page.locator('#restore-file').setInputFiles(join(directory,'card.png'));
  await page.locator('#restore-password').fill('Incorrect disposable password');
  await page.locator('#restore-form button').click();
  await page.waitForFunction(() => document.querySelector('#status').textContent.includes('Неверный пароль'));
  assert.equal(await stored(page), null);
  await page.locator('#restore-file').setInputFiles(join(directory,'card.png'));
  await page.locator('#restore-password').fill('Disposable CI card password 2026');
  await page.locator('#restore-form button').click();
  await page.locator('#participant').waitFor({state:'visible'});
  const restored = await stored(page);
  assert.deepEqual(restored.root, first.root);
  assert.notEqual(restored.grantId, first.grantId);
  assert.ok(restored.nonExtractable && !restored.hasRecovery);
  await context.close(); context = undefined;

  page = await open('clean');
  assert.deepEqual(await stored(page), restored);
  await page.locator('#sign-in').click();
  await page.locator('#participant').waitFor({state:'visible'});
  assert.deepEqual(await stored(page), restored);
  console.log('Chromium: перезапуск профиля, чистый профиль + PNG, неверный пароль без изменения vault, device-only повторный вход — успешно.');
} finally {
  await context?.close();
  await rm(directory,{recursive:true,force:true});
}
