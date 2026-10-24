/* LinkSphere campaign motion: native scrolling, composited image layers, no live device calls. */
(() => {
  'use strict';
  const root = document.documentElement;
  root.classList.add('js');
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');
  const finePointer = matchMedia('(pointer: fine)');
  let frame = 0;
  let userPaused = false;
  const motionButton = document.querySelector('.motion-toggle');
  const paused = () => userPaused || reduced.matches || document.hidden;
  const clamp = (value,min,max) => Math.max(min,Math.min(max,value));
  const scenes = Array.from(document.querySelectorAll('.scroll-scene')).map(section => ({
    section, art: section.querySelector('[data-parallax]'), progress:.5,
    pointerX:0, pointerY:0, x:0, y:0, loaded:false,
    backgrounds:Array.from(section.querySelectorAll('[data-scroll-background]')),
    words:Array.from(section.querySelectorAll('.kinetic-words span')),
    layers:Array.from(section.querySelectorAll('.art-layer')).map(layer => ({
      image:layer.querySelector('img'), depth:Number(layer.dataset.depth),
      shiftX:Number(layer.dataset.shiftX), shiftY:Number(layer.dataset.shiftY),
    })),
  }));
  const render = () => {
    frame = 0;
    if (paused()) { updateMotion(); return; }
    let settling = false;
    const measurements = scenes.map(scene => ({scene,rect:scene.section.getBoundingClientRect()}));
    for (const {scene,rect} of measurements) {
      if (rect.bottom < 0 || rect.top > innerHeight) continue;
      const target = clamp(-rect.top/Math.max(1,rect.height-innerHeight),0,1);
      scene.progress += (target-scene.progress)*.12;
      scene.x += (scene.pointerX-scene.x)*.12;
      scene.y += (scene.pointerY-scene.y)*.12;
      if (Math.abs(target-scene.progress)>.001 || Math.abs(scene.pointerX-scene.x)>.001 || Math.abs(scene.pointerY-scene.y)>.001) settling = true;
      scene.section.style.setProperty('--scene-progress',scene.progress.toFixed(4));
      scene.words.forEach((word,index) => word.classList.toggle('active', index === Math.min(2,Math.floor(scene.progress*3))));
      const progress=(scene.progress-.5)*2;
      const scale=Math.min(1,innerWidth/1000);
      for (const layer of scene.layers) {
        if (!scene.loaded) continue;
        const x=(scene.x*8*layer.depth+progress*layer.shiftX*.45)*scale;
        const y=(scene.y*6*layer.depth+progress*layer.shiftY*.45)*scale;
        layer.image.style.transform=`translate3d(${x.toFixed(2)}px,${y.toFixed(2)}px,0)`;
      }
      scene.backgrounds.forEach(image => { image.style.transform=`translate3d(0,${(-progress*10).toFixed(2)}px,0) scale(${(1.025+scene.progress*.015).toFixed(4)})`; });
    }
    if (settling) frame=requestAnimationFrame(render);
  };
  const schedule=()=>{ if (!frame && !paused()) frame=requestAnimationFrame(render); };
  function updateMotion() {
    const isPaused=paused();
    root.dataset.motion=isPaused?'paused':'playing';
    if (motionButton) {
      motionButton.disabled = reduced.matches;
      motionButton.setAttribute('aria-pressed', String(!isPaused));
      motionButton.title = reduced.matches ? motionButton.dataset.reducedLabel : (isPaused ? motionButton.dataset.playLabel : motionButton.dataset.pauseLabel);
      motionButton.textContent = isPaused ? '▶' : 'Ⅱ';
    }
    if (isPaused) {
      cancelAnimationFrame(frame);frame=0;
      scenes.forEach(scene=>{
        scene.x=scene.y=scene.pointerX=scene.pointerY=0;
        scene.section.style.setProperty('--scene-progress','.5');
        scene.layers.forEach(layer=>{layer.image.style.transform='';});
        scene.backgrounds.forEach(image=>{image.style.transform='';});
      });
    } else schedule();
  }
  if (motionButton) motionButton.addEventListener('click', () => { userPaused = !userPaused; updateMotion(); });
  reduced.addEventListener('change',updateMotion);
  document.addEventListener('visibilitychange',updateMotion);
  window.addEventListener('scroll',()=>{if(paused())updateMotion();else schedule();},{passive:true});
  window.addEventListener('resize',updateMotion,{passive:true});
  scenes.forEach(scene=>{
    if (!scene.art || !scene.layers.length) return;
    Promise.all(scene.layers.map(layer=>layer.image.decode())).then(()=>{
      scene.loaded=true;scene.art.classList.add('layers-ready');schedule();
    }).catch(()=>{/* Complete campaign photography remains visible if a layer cannot load. */});
    scene.art.addEventListener('pointermove',event=>{
      if (!finePointer.matches || event.pointerType==='touch' || paused())return;
      const rect=scene.art.getBoundingClientRect();
      scene.pointerX=clamp((event.clientX-rect.left)/rect.width-.5,-.5,.5);
      scene.pointerY=clamp((event.clientY-rect.top)/rect.height-.5,-.5,.5);
      schedule();
    },{passive:true});
    scene.art.addEventListener('pointerleave',()=>{scene.pointerX=scene.pointerY=0;schedule();});
  });
  const menuButton=document.querySelector('.mobile-menu-btn');
  const menu=document.querySelector('.mobile-links');
  const setMenu=open=>{menu.classList.toggle('open',open);menuButton.setAttribute('aria-expanded',String(open));};
  if (menuButton && menu) {
    menuButton.addEventListener('click',()=>setMenu(menuButton.getAttribute('aria-expanded')!=='true'));
    menu.addEventListener('click',event=>{if(event.target.closest('a'))setMenu(false);});
    document.addEventListener('keydown',event=>{if(event.key==='Escape'&&menuButton.getAttribute('aria-expanded')==='true'){setMenu(false);menuButton.focus();}});
    document.addEventListener('click',event=>{if(!event.target.closest('.nav'))setMenu(false);});
    window.addEventListener('resize',()=>{if(innerWidth>700)setMenu(false);});
  }
  document.querySelectorAll('a[href="#devices"]').forEach(link=>link.addEventListener('click',()=>{document.querySelector('#devices').open=true;}));
  if ('IntersectionObserver' in window) {
    const observer=new IntersectionObserver(entries=>entries.forEach(entry=>{
      if(entry.isIntersecting){entry.target.classList.add('visible');observer.unobserve(entry.target);}
    }),{threshold:.15});
    document.querySelectorAll('.reveal').forEach(el=>observer.observe(el));
    const visibility=new IntersectionObserver(entries=>entries.forEach(entry=>entry.target.classList.toggle('offscreen',!entry.isIntersecting)));
    document.querySelectorAll('.scroll-scene').forEach(el=>visibility.observe(el));
  } else document.querySelectorAll('.reveal').forEach(el=>el.classList.add('visible'));
  updateMotion();
})();
