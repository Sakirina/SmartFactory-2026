// 六大组件深入页 共用脚本：滚动揭示 + 图示灯箱
(function () {
  const obs = new IntersectionObserver((entries) => {
    entries.forEach(e => { if (e.isIntersecting) { e.target.classList.add('visible'); obs.unobserve(e.target); } });
  }, { threshold: 0.1 });
  document.querySelectorAll('.reveal').forEach(el => obs.observe(el));

  const lb = document.getElementById('lightbox');
  if (!lb) return;
  const lbImg = document.getElementById('lightbox-img');
  const lbCap = document.getElementById('lightbox-cap');
  document.querySelectorAll('.diagram-frame').forEach(frame => {
    frame.addEventListener('click', () => {
      const img = frame.querySelector('img');
      lbImg.src = img.getAttribute('src');
      lbCap.textContent = frame.getAttribute('data-cap') || '';
      lb.classList.add('open');
      lb.setAttribute('aria-hidden', 'false');
    });
  });
  function close() { lb.classList.remove('open'); lb.setAttribute('aria-hidden', 'true'); lbImg.src = ''; }
  lb.addEventListener('click', close);
  document.addEventListener('keydown', e => { if (e.key === 'Escape') close(); });
})();
