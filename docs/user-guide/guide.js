'use strict';
(() => {
  const root = document.documentElement;
  const sections = [...document.querySelectorAll('section.chapter')];
  const navLinks = [...document.querySelectorAll('#chapter-nav a')];
  const search = document.getElementById('search');
  const results = document.getElementById('search-results');
  const status = document.getElementById('search-status');
  const theme = document.getElementById('theme');
  const menu = document.getElementById('menu-toggle');
  const progress = document.getElementById('reading-progress');
  const toc = document.getElementById('section-toc');
  const media = window.matchMedia('(prefers-color-scheme: dark)');
  const mobile = window.matchMedia('(max-width: 760px)');
  const normalize = text => text.normalize('NFKC').toLocaleLowerCase('ko').replace(/\s+/g, ' ').trim();
  const index = sections.map(section => ({
    section,
    title: section.querySelector('h2').textContent,
    text: section.textContent.replace(/\s+/g, ' ').trim(),
    searchable: normalize(`${section.dataset.keywords || ''} ${section.textContent} ${navLinks.find(link => link.hash === `#${section.id}`)?.textContent || ''}`),
  }));
  const setTheme = value => {
    root.dataset.theme = value;
    const dark = value === 'dark';
    theme.textContent = dark ? '라이트 모드' : '다크 모드';
    theme.setAttribute('aria-label', dark ? '라이트 모드로 바꾸기' : '다크 모드로 바꾸기');
    theme.setAttribute('aria-pressed', String(dark));
  };
  let savedTheme = null;
  try { savedTheme = localStorage.getItem('ttobak-guide-theme'); } catch { /* Reading works without storage. */ }
  setTheme(savedTheme === 'dark' || savedTheme === 'light' ? savedTheme : media.matches ? 'dark' : 'light');
  theme.hidden = false;
  theme.addEventListener('click', () => {
    const value = root.dataset.theme === 'dark' ? 'light' : 'dark';
    setTheme(value);
    try { localStorage.setItem('ttobak-guide-theme', value); savedTheme = value; } catch { /* Session-only theme. */ }
  });
  media.addEventListener('change', event => { if (!savedTheme) setTheme(event.matches ? 'dark' : 'light'); });
  const closeMenu = returnFocus => {
    root.classList.remove('menu-open');
    menu.setAttribute('aria-expanded', 'false');
    menu.textContent = '목차';
    if (returnFocus) menu.focus();
  };
  menu.hidden = false;
  menu.addEventListener('click', () => {
    const open = !root.classList.contains('menu-open');
    root.classList.toggle('menu-open', open);
    menu.setAttribute('aria-expanded', String(open));
    menu.textContent = open ? '닫기' : '목차';
    if (open) search.focus();
  });
  document.getElementById('contents').addEventListener('click', event => {
    const anchor = event.target.closest('a[href^="#"]');
    if (anchor && mobile.matches) {
      closeMenu(false);
      const target = document.getElementById(anchor.hash.slice(1));
      if (target) {
        target.setAttribute('tabindex', '-1');
        target.focus({ preventScroll: true });
      }
    }
  });
  mobile.addEventListener('change', () => closeMenu(false));
  document.addEventListener('keydown', event => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
      event.preventDefault();
      if (mobile.matches) { root.classList.add('menu-open'); menu.setAttribute('aria-expanded', 'true'); menu.textContent = '닫기'; }
      search.focus(); search.select();
    }
    if (event.key === 'Escape') {
      if (root.classList.contains('menu-open')) closeMenu(true);
      else if (document.activeElement === search) { search.value = ''; renderSearch(); }
    }
    // Keep keyboard focus within the opened mobile contents and its close button.
    if (event.key === 'Tab' && mobile.matches && root.classList.contains('menu-open')) {
      const focusable = [menu, ...document.querySelectorAll('#contents input, #contents a')].filter(node => !node.disabled && node.getClientRects().length);
      const first = focusable[0], last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    }
  });
  const renderSearch = () => {
    const words = normalize(search.value).split(' ').filter(Boolean);
    results.replaceChildren();
    if (!words.length) { results.hidden = true; status.textContent = '본문·메뉴·용어를 검색하세요'; return; }
    const matches = index.filter(item => words.every(word => item.searchable.includes(word)));
    results.hidden = false;
    const heading = document.createElement('h2');
    heading.textContent = `검색 결과 · ${matches.length}개 장`;
    results.append(heading);
    if (!matches.length) {
      const empty = document.createElement('p');
      empty.textContent = '일치하는 내용이 없습니다. 다른 용어나 짧은 검색어를 사용하세요.';
      results.append(empty);
    } else {
      const list = document.createElement('ol');
      for (const item of matches) {
        const entry = document.createElement('li');
        const link = document.createElement('a');
        link.href = `#${item.section.id}`;
        link.textContent = item.title;
        const snippet = document.createElement('p');
        const location = normalize(item.text).indexOf(words[0]);
        const start = Math.max(0, location - 36);
        snippet.textContent = `${start ? '… ' : ''}${item.text.slice(start, start + 180)}${start + 180 < item.text.length ? ' …' : ''}`;
        entry.append(link, snippet); list.append(entry);
      }
      results.append(list);
    }
    status.textContent = `${matches.length}개 장에서 찾았습니다. 검색 결과는 본문 위에 표시됩니다.`;
  };
  search.disabled = false;
  search.addEventListener('input', renderSearch);
  search.addEventListener('keydown', event => {
    if (event.key === 'Enter' && !event.isComposing && event.keyCode !== 229 && search.value.trim()) {
      event.preventDefault(); closeMenu(false);
      results.scrollIntoView({ block: 'start' });
      results.setAttribute('tabindex', '-1'); results.focus({ preventScroll: true });
    }
  });
  renderSearch();
  const print = document.getElementById('print');
  print.hidden = false;
  print.addEventListener('click', () => window.print());
  for (const button of document.querySelectorAll('[data-copy]')) {
    button.hidden = false;
    button.addEventListener('click', async () => {
      const text = document.getElementById(button.dataset.copy).textContent;
      try {
        await navigator.clipboard.writeText(text);
        document.getElementById('copy-status').textContent = '질문 예시를 복사했습니다.';
        button.textContent = '복사 완료';
        window.setTimeout(() => { button.textContent = '예시 복사'; }, 2000);
      } catch {
        document.getElementById('copy-status').textContent = '자동 복사를 사용할 수 없습니다. 예시 본문을 직접 선택해 복사하세요.';
      }
    });
  }
  let active = null;
  const updatePosition = () => {
    const current = [...sections].reverse().find(section => section.getBoundingClientRect().top <= 150) || sections[0];
    if (current !== active) {
      active = current;
      for (const link of navLinks) {
        if (link.hash === `#${current.id}`) link.setAttribute('aria-current', 'true');
        else link.removeAttribute('aria-current');
      }
      toc.replaceChildren();
      for (const heading of current.querySelectorAll('h3[id]')) {
        const link = document.createElement('a'); link.href = `#${heading.id}`; link.textContent = heading.textContent; toc.append(link);
      }
      document.querySelector('.page-toc').hidden = !toc.children.length;
    }
    const max = document.documentElement.scrollHeight - window.innerHeight;
    progress.value = max > 0 ? Math.min(100, window.scrollY / max * 100) : 100;
  };
  let scheduled = false;
  window.addEventListener('scroll', () => {
    if (!scheduled) { scheduled = true; requestAnimationFrame(() => { updatePosition(); scheduled = false; }); }
  }, { passive: true });
  window.addEventListener('resize', updatePosition);
  progress.hidden = false;
  document.querySelector('.page-toc').hidden = false;
  root.classList.add('js-ready');
  updatePosition();
})();
