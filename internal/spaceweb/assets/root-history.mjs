const encoder=new TextEncoder();
const url=bytes=>btoa(String.fromCharCode(...new Uint8Array(bytes))).replaceAll('+','-').replaceAll('/','_').replaceAll('=','');
const decode=(value,length)=>{
  if(typeof value!=='string'||!/^[A-Za-z0-9_-]+$/.test(value))throw new Error('Повреждена история root');
  const data=Uint8Array.from(atob(value.replaceAll('-','+').replaceAll('_','/')),c=>c.charCodeAt(0));
  if(url(data)!==value||(length&&data.length!==length))throw new Error('Повреждена история root');
  return data;
};
const principal=async root=>'u_'+Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',root)),b=>b.toString(16).padStart(2,'0')).join('');
const fields='auth_epoch,challenge_id,expires_at,issued_at,new_device_public_key,new_root_public_key,nonce,old_root_public_key,operation_id,origin,principal_id,purpose,scopes,server_id,v';
export async function verifyRootHistory(history,currentRoot,origin,serverId){
  if(!Array.isArray(history)||history.length>16||currentRoot.length!==32)throw new Error('История root слишком большая или повреждена');
  if(!history.length)return{principalId:await principal(currentRoot),epoch:1};
  let id,previous,issued=0;const seen=new Set();
  for(let i=0;i<history.length;i++){
    const proof=history[i],raw=decode(proof.transcript);if(raw.length>2048)throw new Error('Доказательство слишком большое');
    const text=new TextDecoder('utf-8',{fatal:true}).decode(raw),t=JSON.parse(text),names=Object.keys(t).sort();
    const oldRoot=decode(t.old_root_public_key,32),next=decode(t.new_root_public_key,32),device=decode(t.new_device_public_key,32);
    id??=await principal(oldRoot);if(i===0)seen.add(url(oldRoot));
    if(seen.has(url(next)))throw new Error('История повторно использует root');seen.add(url(next));
    const scope=JSON.stringify(t.scopes),now=Math.floor(Date.now()/1000);
    if(names.join(',')!==fields||JSON.stringify(Object.fromEntries(names.map(k=>[k,t[k]])))!==text||
      !['auth_epoch','issued_at','expires_at','v'].every(k=>Number.isSafeInteger(t[k])&&t[k]>0&&t[k]<9007199254740991)||
      t.v!==1||t.purpose!=='identity.root.rotate'||t.auth_epoch!==i+1||t.principal_id!==id||
      t.origin!==origin||t.server_id!==serverId||(previous&&url(oldRoot)!==previous)||
      url(next)===url(oldRoot)||url(device)===url(next)||url(device)===url(oldRoot)||t.issued_at<issued||t.issued_at>now+5||
      t.expires_at<=t.issued_at||t.expires_at-t.issued_at>120||!/^rc_[A-Za-z0-9_-]{43}$/.test(t.challenge_id)||
      !/^ro_[A-Za-z0-9_-]{43}$/.test(t.operation_id)||decode(t.nonce,32).length!==32||
      (scope!=='["chat.read","chat.write"]'&&scope!=='["chat.read","chat.write","space.manage"]'))throw new Error('История root не соответствует идентичности');
    for(const role of ['old','new']){
      const key=await crypto.subtle.importKey('raw',role==='old'?oldRoot:next,'Ed25519',true,['verify']);
      const prefix=encoder.encode(`space/root.rotate/${role}/v1\0`),bytes=new Uint8Array(prefix.length+raw.length);bytes.set(prefix);bytes.set(raw,prefix.length);
      if(!await crypto.subtle.verify('Ed25519',key,decode(proof[`${role}_signature`],64),bytes))throw new Error('Подпись смены root недействительна');
    }
    previous=url(next);issued=t.issued_at;
  }
  if(previous!==url(currentRoot))throw new Error('Текущий ключ не завершает историю root');
  return{principalId:id,epoch:history.length+1};
}
