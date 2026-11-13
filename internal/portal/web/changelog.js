// The changelog page: static content, only the header needs code (CSP forbids inline).
'use strict';
document.documentElement.classList.add('js');

I18N.ready.then(() => {
  document.getElementById('theme-toggle').addEventListener('click', THEME.toggle);
  const nav = document.querySelector('.nav');
  const onScroll = () => nav.classList.toggle('stuck', scrollY > 8);
  addEventListener('scroll', onScroll, { passive: true });
  onScroll();
});
