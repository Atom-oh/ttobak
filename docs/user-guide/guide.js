'use strict';
const search = document.querySelector('#search');
const links = [...document.querySelectorAll('nav a')];
const status = document.querySelector('#search-status');
search.disabled = false;
search.addEventListener('input', () => {
  const query = search.value.trim().toLocaleLowerCase('ko');
  let count = 0;
  for (const link of links) {
    const section = document.querySelector(link.getAttribute('href'));
    const matches = !query || section.textContent.toLocaleLowerCase('ko').includes(query)
      || link.textContent.toLocaleLowerCase('ko').includes(query);
    link.hidden = !matches;
    if (matches) count += 1;
  }
  status.textContent = query ? `${count}개 장을 찾았습니다. 목차를 눌러 이동하세요.` : '';
});
function markCurrent() {
  for (const link of links) {
    if (link.hash === location.hash) link.setAttribute('aria-current', 'location');
    else link.removeAttribute('aria-current');
  }
}
window.addEventListener('hashchange', markCurrent);
markCurrent();
const printButton = document.querySelector('#print');
printButton.hidden = false;
printButton.addEventListener('click', () => window.print());
