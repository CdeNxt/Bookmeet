// @ts-nocheck
// Local, finite effects. No network calls, audio, timers left running, or click interception.
(function () {
  'use strict';
  if (typeof window === 'undefined' || window.BookMeetEffects) return;
  var motion = window.matchMedia('(prefers-reduced-motion: reduce)');
  var stop = function () {};
  window.BookMeetEffects = function (mode) {
    stop();
    if (mode === 'off' || motion.matches || document.hidden) return;
    if (!['confetti', 'unicorns', 'both', 'sad'].includes(mode)) return;
    var layer = document.createElement('div');
    layer.className = 'bm-celebration';
    layer.setAttribute('aria-hidden', 'true');
    layer.dataset.effect = mode;
    layer.style.cssText = 'position:fixed;inset:0;overflow:hidden;pointer-events:none;z-index:2147483000;contain:strict;';
    var style = document.createElement('style');
    style.textContent = '@keyframes bm-fall{0%{transform:translate3d(0,-70px,0) rotate(0);opacity:0}8%{opacity:1}85%{opacity:1}100%{transform:translate3d(var(--drift),calc(100vh + 90px),0) rotate(var(--spin));opacity:0}}@keyframes bm-fly{0%{transform:translate3d(-120px,35px,0) rotate(-15deg);opacity:0}8%{opacity:1}45%{transform:translate3d(48vw,-65px,0) rotate(8deg)}90%{opacity:1}100%{transform:translate3d(calc(100vw + 120px),-25px,0) rotate(-12deg);opacity:0}}';
    layer.appendChild(style);
    var mobile = innerWidth < 700;
    var count = mode === 'sad' ? (mobile ? 20 : 34) : mode === 'unicorns' ? 0 : (mobile ? 48 : 82);
    var colors = ['#a5b4fc', '#c4b5fd', '#f9a8d4', '#7dd3fc', '#fde68a', '#ffffff'];
    function piece(text, i, flying) {
      var p = document.createElement('span');
      p.textContent = text;
      p.style.cssText = 'position:absolute;display:block;will-change:transform;user-select:none;';
      p.style.left = flying ? '0' : Math.random() * 100 + '%';
      p.style.top = flying ? 15 + Math.random() * 55 + '%' : '-40px';
      p.style.setProperty('--drift', (Math.random() - .5) * 320 + 'px');
      p.style.setProperty('--spin', (Math.random() - .5) * (text ? 80 : 1100) + 'deg');
      if (text) p.style.fontSize = (flying ? 32 + Math.random() * 22 : 23 + Math.random() * 13) + 'px';
      else {
        p.style.width = 5 + Math.random() * 5 + 'px';
        p.style.height = 7 + Math.random() * 7 + 'px';
        p.style.borderRadius = i % 3 === 0 ? '50%' : '2px';
        p.style.background = colors[i % colors.length];
      }
      p.style.animation = (flying ? 'bm-fly' : 'bm-fall') + ' ' + (flying ? 3.5 : 3.2 + Math.random() * 1.3) + 's ' + (i % (flying ? 8 : 15)) * (flying ? .15 : .055) + 's cubic-bezier(.2,.6,.4,1) both';
      layer.appendChild(p);
    }
    for (var i = 0; i < count; i++) piece(mode === 'sad' ? ['😢', '😭', '🥺'][i % 3] : '', i, false);
    if (mode === 'both' || mode === 'unicorns') for (var u = 0; u < (mobile ? 5 : 8); u++) piece('🦄', u, true);
    document.body.appendChild(layer);
    var timer;
    stop = function () {
      clearTimeout(timer); layer.remove();
      document.removeEventListener('visibilitychange', hidden);
      motion.removeEventListener('change', preference);
      window.removeEventListener('pagehide', stop);
    };
    function hidden() { if (document.hidden) stop(); }
    function preference() { if (motion.matches) stop(); }
    document.addEventListener('visibilitychange', hidden);
    motion.addEventListener('change', preference);
    window.addEventListener('pagehide', stop, {once:true});
    timer = setTimeout(stop, 5800);
  };
  window.addEventListener('bookmeet:booked', function () {
    var settings = document.getElementById('bm-experience');
    if (settings) window.BookMeetEffects(settings.dataset.booked);
  });
  window.addEventListener('bookmeet:cancelled', function () {
    var settings = document.getElementById('bm-experience');
    if (settings) window.BookMeetEffects(settings.dataset.cancelled);
  });
}());
