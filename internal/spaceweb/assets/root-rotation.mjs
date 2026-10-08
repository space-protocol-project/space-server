import {newKeys,request,base64,url64} from './identity.mjs';
import {verifyRootHistory} from './root-history.mjs';
const encoder=new TextEncoder();
const decode=v=>Uint8Array.from(atob(v.replaceAll('-','+').replaceAll('_','/')),c=>c.charCodeAt(0));
const pub=async pair=>new Uint8Array(await crypto.subtle.exportKey('raw',pair.publicKey));
export async function rotationSnapshot(record){
 const value={root:url64(await pub(record.root)),device:url64(await pub(record.device)),grantId:record.grantId,serverId:record.serverId,serverKey:record.serverKey,history:record.rootHistory||[]};
 return url64(await crypto.subtle.digest('SHA-256',encoder.encode(JSON.stringify(value))));
}
export async function prepareBrowserRotation(origin,discovery,current,token,existing,renew=false){
 if(!current.root.privateKey)throw new Error('Нужен исходный корневой ключ этого браузера');
 if(existing&&!renew)throw new Error('Сначала завершите сохранённую смену');
 if(renew&&!existing)throw new Error('Нет сохранённой смены ключа');
 const root=await pub(current.root),identity=await verifyRootHistory(current.rootHistory||[],root,origin,discovery.server_id);
 if(identity.epoch>16)throw new Error('Достигнут предел экспериментальной истории');
 const next=existing?existing.next:await newKeys();
 let operation=existing?.operation;
 if(existing){
   if(await rotationSnapshot(current)!==existing.oldSnapshot)throw new Error('Активная идентичность изменилась');
   await verifyRootHistory(next.rootHistory,await pub(next.root),origin,discovery.server_id);
 }else operation='ro_'+url64(crypto.getRandomValues(new Uint8Array(32)));
 const nextRoot=await pub(next.root),nextDevice=await pub(next.device);
 const challenge=await request(origin,'/api/v1/auth/root-rotations',{profile:'root-rotation-v1',operationId:operation,expectedAuthEpoch:String(identity.epoch),newRootPublicKey:base64(nextRoot),newDevicePublicKey:base64(nextDevice)},token);
 const raw=decode(challenge.transcript),t=JSON.parse(new TextDecoder().decode(raw));
 if(t.challenge_id!==challenge.challengeId||t.operation_id!==operation||t.principal_id!==identity.principalId||t.auth_epoch!==identity.epoch||t.old_root_public_key!==url64(root)||t.new_root_public_key!==url64(nextRoot)||t.new_device_public_key!==url64(nextDevice)||t.expires_at<=Date.now()/1000)throw new Error('Запрос не соответствует выбранной смене');
 const cert={transcript:url64(raw)};
 for(const role of ['old','new']){
   const prefix=encoder.encode(`space/root.rotate/${role}/v1\0`),bytes=new Uint8Array(prefix.length+raw.length);bytes.set(prefix);bytes.set(raw,prefix.length);
   cert[`${role}_signature`]=url64(await crypto.subtle.sign('Ed25519',role==='old'?current.root.privateKey:next.root.privateKey,bytes));
 }
 next.rootHistory=[...(current.rootHistory||[]),cert];next.serverId=current.serverId;next.serverKey=current.serverKey;next.grantId='';
 await verifyRootHistory(next.rootHistory,nextRoot,origin,discovery.server_id);
 if(encoder.encode(JSON.stringify(next.rootHistory)).length>7000)throw new Error('История не помещается в recovery-карточку');
 return{v:1,operation,oldSnapshot:await rotationSnapshot(current),next};
}
export async function commitBrowserRotation(origin,discovery,pending,current){
 if(pending?.v!==1||pending.next.serverId!==discovery.server_id||pending.next.serverKey!==discovery.signing_public_key)throw new Error('Журнал относится к другому серверу');
 const next=pending.next,identity=await verifyRootHistory(next.rootHistory,await pub(next.root),origin,discovery.server_id);
 const cert=next.rootHistory.at(-1),t=JSON.parse(new TextDecoder().decode(decode(cert.transcript)));
 if(url64(await pub(next.device))!==t.new_device_public_key)throw new Error('Ключ устройства изменён');
 const sameNew=current&&url64(await pub(current.root))===url64(await pub(next.root))&&url64(await pub(current.device))===url64(await pub(next.device))&&JSON.stringify(current.rootHistory)===JSON.stringify(next.rootHistory);
 if(current&&await rotationSnapshot(current)!==pending.oldSnapshot&&!sameNew)throw new Error('Активная идентичность изменилась');
 const receipt=await request(origin,'/api/v1/auth/root-rotations/complete',{challengeId:t.challenge_id,oldSignature:base64(decode(cert.old_signature)),newSignature:base64(decode(cert.new_signature))});
 if(url64(decode(receipt.transcript))!==cert.transcript||url64(decode(receipt.oldSignature))!==cert.old_signature||url64(decode(receipt.newSignature))!==cert.new_signature||receipt.principalId!==identity.principalId||Number(receipt.authEpoch)!==identity.epoch||!/^dg_[A-Za-z0-9_-]{43}$/.test(receipt.grantId))throw new Error('Ответ не соответствует сохранённой смене');
 next.grantId=receipt.grantId;return next;
}
