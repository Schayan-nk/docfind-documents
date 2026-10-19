// One document of the archive in the Word editor (office.go): /office?id=<doc>.
// docfind signs the editor's configuration; this page only loads the editor
// through docfind's relay and hands it over. Saving happens between the
// document server and docfind -- the editor saves as people type, and the
// file in the archive is replaced (the old one kept) when the Save button is
// pressed or everybody has closed it.
(function () {
  'use strict';
  const $ = id => document.getElementById(id);
  const fa = s => String(s).replace(/\d/g, c => '۰۱۲۳۴۵۶۷۸۹'[c]);
  const pad = n => String(n).padStart(2, '0');
  const id = new URLSearchParams(location.search).get('id') || '';
  let config;

  function fail(text) {
    const m = $('ofMsg');
    m.textContent = text;
    m.hidden = false;
    $('ofEditor').textContent = '';
  }
  async function api(path, opts) {
    const r = await fetch(path, Object.assign({ credentials: 'same-origin' }, opts || {}));
    if (r.status === 401) { location.replace('/login?next=' + encodeURIComponent(location.pathname + location.search)); throw new Error('signed out'); }
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || 'انجام نشد.');
    return d;
  }
  function load(src) {
    return new Promise((ok, no) => {
      const s = document.createElement('script');
      s.src = src;
      s.onload = ok;
      s.onerror = () => no(new Error('ویرایشگر Word پاسخ نداد. اگر تازه راه‌اندازی شده، چند دقیقه بعد دوباره بازش کنید.'));
      document.head.append(s);
    });
  }
  function when(iso) {
    const d = new Date(iso);
    const DP = window.docfindDatePicker;
    const day = DP ? DP.gregorianToJalali(d.getFullYear(), d.getMonth() + 1, d.getDate()).map(pad).join('/') : d.toISOString().slice(0, 10);
    return fa(day + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()));
  }

  async function open() {
    if (!id) { fail('سندی برای باز کردن نام برده نشده است.'); return; }
    const mobile = matchMedia('(max-width: 720px)').matches ? '&mobile=1' : '';
    let d;
    try { d = await api('/api/office/config?id=' + encodeURIComponent(id) + mobile); } catch (e) { fail(e.message); return; }
    config = d.config;
    document.title = d.name + ' | ویرایشگر Word';
    $('ofName').textContent = d.name;
    $('ofName').title = d.id;
    $('ofMode').textContent = d.edit ? 'ویرایش · ذخیره با دکمهٔ ذخیره یا با بستن' : 'فقط خواندن';
    $('ofBack').href = '/?q=' + encodeURIComponent(d.ref || '');
    $('ofVersions').hidden = !d.versions;
    try { await load(d.api); } catch (e) { fail(e.message); return; }
    if (!window.DocsAPI) { fail('ویرایشگر Word بارگذاری نشد.'); return; }
    config.events = {
      onError: ev => fail('ویرایشگر: ' + ((ev && ev.data && ev.data.errorDescription) || 'خطا')),
      onRequestClose: () => history.length > 1 ? history.back() : location.assign($('ofBack').href),
    };
    $('ofEditor').textContent = '';
    new window.DocsAPI.DocEditor('ofEditor', config);
  }

  async function versions() {
    const list = $('ofVerList');
    list.textContent = '';
    let d;
    try { d = await api('/api/office/versions?id=' + encodeURIComponent(id)); } catch (e) { fail(e.message); return; }
    if (!d.versions.length) list.append(Object.assign(document.createElement('li'), { textContent: 'هنوز نسخهٔ پیشینی نیست.' }));
    for (const v of d.versions) {
      const li = document.createElement('li');
      const what = document.createElement('span');
      what.textContent = when(v.at) + ' · ' + v.reason + (v.by ? ' (' + v.by + ')' : '') + ' · ' + fa(Math.max(1, Math.round(v.size / 1024))) + ' KB';
      const dl = document.createElement('a');
      dl.className = 'btn btn-sm';
      dl.textContent = 'دریافت';
      dl.href = '/api/office/version?id=' + encodeURIComponent(id) + '&v=' + encodeURIComponent(v.v);
      li.append(what, dl);
      if (d.can_restore) {
        const back = document.createElement('button');
        back.type = 'button';
        back.className = 'btn btn-sm btn-ghost';
        back.textContent = 'بازگرداندن';
        let armed = false;
        back.addEventListener('click', async () => {
          // Two presses: the first says what will happen.
          if (!armed) { armed = true; back.textContent = 'مطمئنید؟ سند فعلی هم نگه داشته می‌شود'; return; }
          back.disabled = true;
          try {
            await api('/api/office/restore', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, v: v.v }) });
            location.reload();
          } catch (e) { back.disabled = false; fail(e.message); }
        });
        li.append(back);
      }
      list.append(li);
    }
  }

  $('ofVersions').addEventListener('click', () => { versions(); $('ofVer').showModal(); });
  $('ofVerClose').addEventListener('click', () => $('ofVer').close());
  open();
})();
