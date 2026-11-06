// Portable extraction of the live Bookmeet painter. Load with defer.
(()=>{
  const canvas=document.createElement('canvas');canvas.id='bm-field';canvas.setAttribute('aria-hidden','true');document.body.prepend(canvas);
  const ctx=canvas.getContext('2d');if(!ctx)return;
  const reduced=matchMedia('(prefers-reduced-motion: reduce)');
  let width=innerWidth+40,height=innerHeight+40,raf=0,last=0,start=performance.now(),px=0,py=0,tx=0,ty=0,pointerX=-1000,pointerY=-1000;
  const stars=Array.from({length:520},(_,i)=>({x:((i*7919)%997)/997,y:((i*6271)%991)/991,a:.08+((i*13)%13)/100}));
  function resize(){width=innerWidth+40;height=innerHeight+40;const d=Math.min(devicePixelRatio||1,1.5);canvas.width=Math.round(width*d);canvas.height=Math.round(height*d);ctx.setTransform(d,0,0,d,0,0);if(reduced.matches)paint(0)}
  function paint(time){
   px+=(tx-px)*.055;py+=(ty-py)*.055;
   canvas.style.transform='translate3d('+(-px*10).toFixed(2)+'px,'+(-py*7).toFixed(2)+'px,0)';
   // One painter in both modes: the light canvas is its exact CSS color inverse.
   ctx.fillStyle='#15161e';ctx.fillRect(0,0,width,height);
   for(const s of stars){ctx.fillStyle='rgba(188,195,220,'+s.a+')';ctx.fillRect(s.x*width+px*9,s.y*height+py*6,1,1)}
   const mobile=width<741,planes=mobile?30:48,half=Math.max(width*.43,mobile?260:440),depth=half*.33;
   for(let j=0;j<planes;j++){
    const z=(j/(planes-1)-.5)*2,angle=z*.64+.28*Math.sin(z*2.1+time)+time*.08+px*.22;
    const span=half*(1+.14*Math.abs(z)),cy=height*.48+z*height*.70+py*20;
    const corners=[[-span,-depth],[span,-depth],[span,depth],[-span,depth]].map(([x,y])=>[width*.5+x*Math.cos(angle)-y*Math.sin(angle)+px*45,cy+(x*Math.sin(angle)+y*Math.cos(angle))*(.47+py*.025)]);
    for(let k=0;k<4;k++){
     const a=corners[k],b=corners[(k+1)%4],n=Math.min(mobile?78:125,Math.max(10,Math.floor(Math.hypot(b[0]-a[0],b[1]-a[1])/4)));
     for(let q=0;q<n;q++){
      const f=q/n;let x=a[0]+(b[0]-a[0])*f,y=a[1]+(b[1]-a[1])*f;
      const dx=x-pointerX,dy=y-pointerY,dist=Math.hypot(dx,dy),near=Math.max(0,1-dist/200);
      if(dist>1&&near>0){x+=dx/dist*near*14;y+=dy/dist*near*14}
      const quiet=Math.min(1,.48+Math.abs(x-width*.5)/(width*.38));
      const alpha=(.20+.24*(.5+.5*Math.sin(q*.87+j*1.4+time*2))+near*.24)*quiet;
      ctx.fillStyle='rgba(188,194,220,'+alpha+')';const size=near>.1?1.15:.8;ctx.fillRect(x,y,size,size);
     }
    }
   }
   const shade=ctx.createRadialGradient(width*.5,height*.34,0,width*.5,height*.34,width*.54);shade.addColorStop(0,'rgba(21,22,30,.34)');shade.addColorStop(1,'rgba(21,22,30,0)');ctx.fillStyle=shade;ctx.fillRect(0,0,width,height);
  }
  function frame(now){raf=0;if(document.hidden||reduced.matches)return;if(now-last>40){last=now;paint((now-start)*.00012)}raf=requestAnimationFrame(frame)}
  function resume(){if(!document.hidden&&!reduced.matches&&!raf)raf=requestAnimationFrame(frame)}
  addEventListener('resize',resize,{passive:true});
  addEventListener('pointermove',e=>{if(reduced.matches||e.pointerType==='touch')return;tx=(e.clientX/innerWidth-.5)*2;ty=(e.clientY/innerHeight-.5)*2;pointerX=e.clientX+20;pointerY=e.clientY+20},{passive:true});
  document.addEventListener('pointerleave',()=>{tx=0;ty=0;pointerX=-1000;pointerY=-1000});
  document.addEventListener('visibilitychange',()=>{if(document.hidden&&raf){cancelAnimationFrame(raf);raf=0}else resume()});
  reduced.addEventListener('change',()=>{if(raf){cancelAnimationFrame(raf);raf=0}tx=ty=px=py=0;pointerX=pointerY=-1000;paint(0);resume()});
  document.addEventListener('bookmeet:theme',()=>paint((performance.now()-start)*.00012));
  resize();paint(0);resume();
})();
