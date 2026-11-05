const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const boot = fs.readFileSync(path.join(__dirname, 'bookmeet-skin.html'), 'utf8').match(/<script>([\s\S]*?)<\/script>/)[1];
function setup({ configured='auto', dark=false, stored=null, blocked=false }={}) {
 const root={dataset:{},style:{}};
 const callbacks=[];const writes=[];
 const system={matches:dark,addEventListener:(_event,callback)=>callbacks.push(callback)};
 const context={window:{},document:{documentElement:root,querySelector:()=>({content:configured}),dispatchEvent:()=>{}},matchMedia:()=>system,CustomEvent:function(type){this.type=type},localStorage:{getItem:()=>{if(blocked)throw Error('blocked');return stored},setItem:(key,value)=>{if(blocked)throw Error('blocked');writes.push([key,value])}}};
 vm.runInNewContext(boot,context);
 return {root,theme:context.window.BookMeetTheme,writes,changeSystem(value){system.matches=value;callbacks.forEach(fn=>fn())}};
}
test('Auto follows initial device preference and live preference changes',()=>{
 const app=setup({dark:true});assert.equal(app.root.dataset.bmTheme,'dark');
 app.changeSystem(false);assert.equal(app.root.dataset.bmTheme,'light');
 app.changeSystem(true);assert.equal(app.root.dataset.bmTheme,'dark');
});
test('A visitor override persists and stays fixed until Auto is selected',()=>{
 const app=setup({configured:'dark',stored:'light',dark:true});assert.equal(app.theme.get(),'light');assert.equal(app.root.dataset.bmTheme,'light');
 app.changeSystem(false);assert.equal(app.root.dataset.bmTheme,'light');
 app.theme.set('dark');assert.equal(app.root.dataset.bmTheme,'dark');assert.deepEqual(app.writes.pop(),['bookmeet-appearance','dark']);
 app.theme.set('auto');assert.equal(app.root.dataset.bmTheme,'light');app.changeSystem(true);assert.equal(app.root.dataset.bmTheme,'dark');
});
test('Invalid preferences and unavailable browser storage are safe',()=>{
 const app=setup({configured:'light',stored:'surprise',dark:true});assert.equal(app.root.dataset.bmTheme,'light');app.theme.set('surprise');assert.equal(app.theme.get(),'light');
 const blocked=setup({blocked:true,dark:true});assert.equal(blocked.root.dataset.bmTheme,'dark');blocked.theme.set('light');assert.equal(blocked.root.dataset.bmTheme,'light');
});

test('Light inverts the same canvas without reducing particle contrast',()=>{
 const source=fs.readFileSync(path.join(__dirname,'bookmeet-skin.html'),'utf8');
 assert.match(source,/html\[data-bm-theme="light"\] body\.bm-scene #bm-field\{filter:invert\(1\)\}/);
 assert.doesNotMatch(source,/light\?alpha/);
 assert.match(source,/ctx\.fillStyle='#15161e'/);
});
