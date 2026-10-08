// Управление каналами: текстовые DOM-узлы, без HTML из серверных данных.
export function installChannels({api,run,status,onMetadataChanged}) {
  const $ = id => document.getElementById(id);
  let selected, revision, rules = [];
  const permissionNames = {visible:'Видеть канал',read:'Читать',write:'Отправлять',manage:'Управлять'};
  const node = (tag,text) => { const n=document.createElement(tag); if(text!==undefined)n.textContent=text; return n; };
  const button = (text,action) => {const b=node('button',text);b.type='button';b.className='secondary';b.addEventListener('click',()=>run(action));return b;};
  function show(channel) {
    selected=channel;
    $('channel-editor').hidden=false;
    $('channel-id').textContent=channel.id;
    $('channel-title').value=channel.title;
    $('channel-position').value=channel.position||0;
    $('channel-archived').checked=!!channel.archived;
    $('channel-preview').checked=!!channel.publicPreview;
    $('channel-version').textContent=`Версия ${channel.revision}`;
    $('channel-access').hidden=true;
  }
  async function load() {
    const result=await api('/api/v1/channels?includeArchived=true');
    const list=$('channels-list');list.replaceChildren();
    for(const c of result.channels||[]) {
      const row=node('div');row.className='member-row';
      row.dataset.channelId=c.id;
      const label=node('p',`${c.title} · ${c.id}${c.archived?' · Архив':''}`);
      row.append(label,button('Настроить',async()=>{show(c);await loadAccess();}));
      list.append(row);
      if(selected?.id===c.id)show(c);
    }
    if(!list.childElementCount)list.append(node('p','Каналов пока нет.'));
  }
  function renderRules() {
    const list=$('channel-rules');list.replaceChildren();
    rules.forEach((rule,index)=>{
      const row=node('fieldset');row.className='access-rule';
      row.append(node('legend',rule.role==='member'?'Участники':rule.role==='reader'?'Читатели':rule.principalId));
      const p=rule.permissions||{};rule.permissions=p;
      for(const [key,label] of Object.entries(permissionNames)) {
        const input=node('input');input.type='checkbox';input.checked=!!p[key];
        input.addEventListener('change',()=>{
          p[key]=input.checked;
          if(!input.checked&&key==='visible')Object.assign(p,{read:false,write:false,manage:false});
          if(!input.checked&&key==='read')p.write=false;
          if(p.write)p.read=true;
          if(p.read||p.manage)p.visible=true;
          renderRules();
        });
        const text=node('label');text.className='check';text.append(input,document.createTextNode(label));row.append(text);
      }
      row.append(button('Удалить правило',async()=>{rules.splice(index,1);renderRules();}));
      list.append(row);
    });
    if(!rules.length)list.append(node('p','Правил нет: канал доступен только владельцу и администраторам.'));
  }
  async function loadAccess() {
    if(!selected)return;
    const result=await api(`/api/v1/channels/${encodeURIComponent(selected.id)}/access`);
    revision=result.revision;
    rules=structuredClone(result.rules||[]);
    $('channel-access').hidden=false;
    $('access-version').textContent=`Права: версия ${revision}`;
    renderRules();
  }
  async function conflict(action) {
    try {await action();} catch(e) {
      if(e.status===409)throw new Error('Канал уже изменён. Загрузите его заново перед сохранением. Ваши несохранённые поля пока остаются в форме.');
      throw e;
    }
  }
  $('reload-channels').addEventListener('click',()=>run(async()=>{selected=undefined;$('channel-editor').hidden=true;await load();status('Каналы обновлены.');}));
  $('create-channel').addEventListener('submit',event=>{
    event.preventDefault();run(async()=>{
      const result=await api('/api/v1/channels',{channelId:$('new-channel-id').value.trim(),title:$('new-channel-title').value.trim(),viewType:'chat',position:Number($('new-channel-position').value),publicPreview:false});
      event.target.reset();await load();show(result.channel);await loadAccess();
      status('Канал создан. Участники могут читать и писать, читатели — читать. Настройте права ниже, если нужна закрытая группа.');
    });
  });
  $('channel-form').addEventListener('submit',event=>{
    event.preventDefault();run(()=>conflict(async()=>{
      if(!selected)return;
      const result=await api(`/api/v1/channels/${encodeURIComponent(selected.id)}`,{title:$('channel-title').value.trim(),position:Number($('channel-position').value),archived:$('channel-archived').checked,publicPreview:$('channel-preview').checked,expectedRevision:selected.revision},'PATCH');
      selected=result.channel;await load();await loadAccess();await onMetadataChanged();status('Настройки канала сохранены.');
    }));
  });
  $('reload-channel-access').addEventListener('click',()=>run(async()=>{await load();await loadAccess();status('Канал и права загружены заново.');}));
  $('add-channel-rule').addEventListener('click',()=>run(async()=>{
    if(rules.length>=100)throw new Error('Лимит: 100 правил.');
    const subject=$('rule-subject').value;
    const id=$('rule-principal').value.trim();
    if(subject==='principal'&&!/^u_[0-9a-f]{64}$/.test(id))throw new Error('Укажите полный principal_id участника из списка выше.');
    const rule=subject==='principal'?{principalId:id}:{role:subject};
    if(rules.some(r=>rule.role?r.role===rule.role:r.principalId===rule.principalId))throw new Error('Для этого участника или роли правило уже есть.');
    rules.push({...rule,permissions:{visible:true,read:true,write:subject==='member',manage:false}});renderRules();
  }));
  $('save-channel-access').addEventListener('click',()=>run(()=>conflict(async()=>{
    if(!selected)return;
    const result=await api(`/api/v1/channels/${encodeURIComponent(selected.id)}/access`,{expectedRevision:revision,rules},'PUT');
    selected.revision=result.revision;await load();await loadAccess();status('Права канала сохранены. Сервер проверяет их при чтении, отправке и подписке.');
  })));
  return {load,clear:()=>{selected=undefined;rules=[];$('channels-list').replaceChildren();$('channel-editor').hidden=true;}};
}
