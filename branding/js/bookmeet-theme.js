
(function(){
 const root=document.documentElement;
 const configured=document.querySelector('meta[name="bookmeet-theme"]')?.content||'auto';
 const system=matchMedia('(prefers-color-scheme: dark)');
 let choice=configured;
 try{const saved=localStorage.getItem('bookmeet-appearance');if(['auto','light','dark'].includes(saved))choice=saved}catch(_){}
 function apply(){root.dataset.bmTheme=choice==='auto'?(system.matches?'dark':'light'):choice;root.style.colorScheme=root.dataset.bmTheme;document.dispatchEvent(new CustomEvent('bookmeet:theme'))}
 window.BookMeetTheme={get:()=>choice,set:value=>{if(!['auto','light','dark'].includes(value))return;choice=value;try{localStorage.setItem('bookmeet-appearance',value)}catch(_){}apply()}};
 system.addEventListener('change',()=>{if(choice==='auto')apply()});
 apply();
})();
