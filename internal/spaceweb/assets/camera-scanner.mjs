import {qrText} from './recovery-qr.mjs';

export function cameraMessage(error){
  return ({NotAllowedError:'Доступ к камере запрещён. Разрешите его в браузере или выберите файл.',NotFoundError:'Камера не найдена. Подключите её или выберите файл.',NotReadableError:'Камера занята другим приложением. Закройте его и повторите.'})[error?.name] || 'Не удалось открыть камеру. Используйте PNG или JSON.';
}
export function installCameraScanner(dialog,onPacket){
  const video=dialog.querySelector('video'),status=dialog.querySelector('[role="status"]'),close=dialog.querySelector('button');
  let stream,timeout,generation=0;
  const stop=()=>{
    generation++;clearTimeout(timeout);
    stream?.getTracks().forEach(track=>track.stop());stream=undefined;
    video.srcObject=null;
  };
  close.addEventListener('click',()=>dialog.close());
  dialog.addEventListener('close',stop);
  dialog.addEventListener('cancel',stop);
  document.addEventListener('visibilitychange',()=>{if(document.hidden&&dialog.open)dialog.close();});
  window.addEventListener('pagehide',stop);
  return async()=>{
    if(dialog.open)return;
    stop();dialog.showModal();status.textContent='Разрешите камеру и наведите её на карточку Space.';
    const token=generation;
    try {
      if(!isSecureContext||!navigator.mediaDevices?.getUserMedia)throw new Error('Unsupported');
      const requested=await navigator.mediaDevices.getUserMedia({audio:false,video:{facingMode:{ideal:'environment'},width:{ideal:1280},height:{ideal:720}}});
      if(!dialog.open||token!==generation){requested.getTracks().forEach(track=>track.stop());return;}
      stream=requested;video.srcObject=stream;await video.play();
      const canvas=document.createElement('canvas'),context=canvas.getContext('2d',{willReadFrequently:true});
      const scan=()=>{
        if(!dialog.open||token!==generation)return;
        if(video.readyState>=2&&video.videoWidth){
          const ratio=Math.min(1,960/Math.max(video.videoWidth,video.videoHeight));
          canvas.width=Math.max(1,Math.round(video.videoWidth*ratio));canvas.height=Math.max(1,Math.round(video.videoHeight*ratio));
          context.drawImage(video,0,0,canvas.width,canvas.height);
          let packet;
          try{packet=qrText(context.getImageData(0,0,canvas.width,canvas.height).data,canvas.width,canvas.height);}catch(_){/* Нет карточки в этом кадре. */}
          if(packet){dialog.close();onPacket(packet);return;}
        }
        timeout=setTimeout(scan,350);
      };
      scan();
    }catch(error){if(token===generation&&dialog.open){stop();status.textContent=cameraMessage(error);}}
  };
}
