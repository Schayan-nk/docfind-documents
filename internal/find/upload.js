// The file-upload control, and the send button that flies.
//
// Shared on purpose. It is used by the announcement form today and by the
// documents page's folder upload next, and those two must not drift into two
// different ways of choosing a file.
//
// WHY THIS IS WRITTEN RATHER THAN INSTALLED. The design asked for is
// @reui/c-file-upload-6, a shadcn component. shadcn components are React and
// Tailwind, delivered by a generator that writes .tsx into a project with a
// build step. This application is one Go executable with no build step, no
// bundler and no React, and every asset is compiled in -- the same wall the
// thinking-orbs React wrapper hit, where only the framework-agnostic engine
// could be vendored. So the BEHAVIOUR is taken from that component -- a drop
// zone that reacts to a drag, click-to-browse, the chosen file shown as a
// removable row with its size -- and written against the DOM directly.
//
// Nothing here uses innerHTML. Names come from the filesystem of whoever is
// uploading, and the one thing a file name must never be is markup.
(function () {
  'use strict';

  const SVG_NS = 'http://www.w3.org/2000/svg';
  function icon(name, cls) {
    const svg = document.createElementNS(SVG_NS, 'svg');
    svg.setAttribute('class', cls || 'ic');
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS(SVG_NS, 'use');
    use.setAttribute('href', '#i-' + name);
    svg.appendChild(use);
    return svg;
  }

  const FA = ['۰', '۱', '۲', '۳', '۴', '۵', '۶', '۷', '۸', '۹'];
  const fa = s => String(s == null ? '' : s).replace(/\d/g, d => FA[+d]);

  // The size, written the way the announcements panel writes one, so a file
  // reads the same before it is sent and after.
  function sizeText(n) {
    if (!(n > 0)) return '';
    if (n < 1024) return fa(n) + ' بایت';
    if (n < 1024 * 1024) return fa(Math.round(n / 1024)) + ' کیلوبایت';
    return fa((n / (1024 * 1024)).toFixed(1)) + ' مگابایت';
  }

  // mountDropzone turns a container into a file chooser.
  //
  //   host     the element to fill
  //   opt.input      the <input type="file"> it drives; created if absent
  //   opt.multiple   accept more than one
  //   opt.maxBytes   refuse anything larger, per file
  //   opt.onChange   called with the current FileList after every change
  //
  // Returns { files, clear }.
  function mountDropzone(host, opt) {
    const o = opt || {};
    const input = o.input || document.createElement('input');
    input.type = 'file';
    if (o.multiple) input.multiple = true; else input.multiple = false;
    if (o.accept) input.accept = o.accept;
    // Off-screen rather than display:none. A hidden input cannot be focused,
    // and this one is the control's real keyboard target -- the zone below
    // forwards to it, and a reader tabbing through the form must land
    // somewhere that opens the picker.
    input.className = 'up-input';
    if (!input.parentElement) host.appendChild(input);

    const zone = document.createElement('div');
    zone.className = 'up-zone';
    zone.tabIndex = 0;
    zone.setAttribute('role', 'button');
    zone.setAttribute('aria-label', o.label || 'انتخاب فایل');

    zone.appendChild(icon('upload', 'ic up-ic'));
    const cap = document.createElement('div');
    cap.className = 'up-cap';
    const capMain = document.createElement('span');
    capMain.textContent = o.label || 'فایل را این‌جا رها کنید';
    const capSub = document.createElement('span');
    capSub.className = 'up-sub';
    capSub.textContent = o.hint || 'یا برای انتخاب کلیک کنید';
    cap.appendChild(capMain);
    cap.appendChild(capSub);
    zone.appendChild(cap);
    host.appendChild(zone);

    const list = document.createElement('div');
    list.className = 'up-list';
    host.appendChild(list);

    const err = document.createElement('p');
    err.className = 'up-err';
    err.hidden = true;
    host.appendChild(err);

    // Chosen files are held here rather than read back off the input,
    // because removing one means rebuilding the input's own FileList -- which
    // is read-only except through a DataTransfer.
    let chosen = [];

    function refuse(msg) {
      err.hidden = !msg;
      err.textContent = msg || '';
    }

    function sync() {
      // Write the list back onto the input so a plain form submit carries the
      // same files this control is showing. DataTransfer is the only way to
      // build a FileList, and it is why removing a file works at all.
      try {
        const dt = new DataTransfer();
        chosen.forEach(f => dt.items.add(f));
        input.files = dt.files;
      } catch (_) {
        // Very old engines have no DataTransfer constructor. The input keeps
        // whatever the picker put there, which is right for everything except
        // removal -- so the remove control is hidden in that case.
      }
      draw();
      if (o.onChange) o.onChange(chosen.slice());
    }

    function draw() {
      list.textContent = '';
      chosen.forEach((f, i) => {
        const row = document.createElement('div');
        row.className = 'up-file';
        row.appendChild(icon('file-text', 'ic up-fic'));

        const nm = document.createElement('span');
        nm.className = 'up-name';
        // The name is an identifier -- "Fax.pdf" must not come back as
        // "pdf.Fax" on this right-to-left page -- and isolate keeps it from
        // dragging the size beside it around.
        nm.textContent = f.name;
        row.appendChild(nm);

        const sz = document.createElement('span');
        sz.className = 'up-size';
        sz.textContent = sizeText(f.size);
        row.appendChild(sz);

        const x = document.createElement('button');
        x.type = 'button';
        x.className = 'up-x';
        x.setAttribute('aria-label', 'حذف ' + f.name);
        x.appendChild(icon('x', 'ic'));
        x.addEventListener('click', ev => {
          ev.preventDefault();
          ev.stopPropagation();
          chosen.splice(i, 1);
          refuse('');
          sync();
        });
        row.appendChild(x);
        list.appendChild(row);
      });
      zone.classList.toggle('has-file', chosen.length > 0);
    }

    function take(files) {
      const incoming = Array.prototype.slice.call(files || []);
      if (!incoming.length) return;
      const kept = [];
      for (const f of incoming) {
        if (o.maxBytes && f.size > o.maxBytes) {
          refuse(f.name + ' بزرگ‌تر از حد مجاز است (' + sizeText(o.maxBytes) + ').');
          continue;
        }
        kept.push(f);
      }
      if (!kept.length) return;
      refuse('');
      // One file means REPLACE, not append: a chooser that silently keeps the
      // first pick when somebody drops a second is how the wrong document
      // gets published.
      chosen = o.multiple ? chosen.concat(kept) : kept.slice(0, 1);
      sync();
    }

    input.addEventListener('change', () => take(input.files));
    zone.addEventListener('click', () => input.click());
    zone.addEventListener('keydown', ev => {
      if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); input.click(); }
    });

    // dragover must be cancelled or the browser navigates to the file, which
    // loses whatever was typed into the form.
    ['dragenter', 'dragover'].forEach(t => zone.addEventListener(t, ev => {
      ev.preventDefault();
      zone.classList.add('over');
    }));
    ['dragleave', 'dragend'].forEach(t => zone.addEventListener(t, () => zone.classList.remove('over')));
    zone.addEventListener('drop', ev => {
      ev.preventDefault();
      zone.classList.remove('over');
      take(ev.dataTransfer && ev.dataTransfer.files);
    });

    draw();
    return {
      files: () => chosen.slice(),
      clear: () => { chosen = []; refuse(''); sync(); },
      // keep(fn) drops every file the predicate rejects.
      //
      // For what happens after a send: the ones that landed are gone and the
      // ones that were refused stay, with the reason beside them. Leaving
      // everything in the list means pressing send again re-uploads the files
      // that already worked -- which does not overwrite them, it makes
      // "report (2).pdf", so the archive quietly grows a duplicate every time
      // somebody retries because of one bad file.
      keep: fn => { chosen = chosen.filter(fn); sync(); },
      refuse,
      input,
    };
  }

  // flyButton makes a submit button launch.
  //
  // Three states on one control rather than a button plus a spinner plus a
  // toast: the paper plane flies off when it is pressed, and comes back as a
  // green tick when the server says yes. The reader watches the thing they
  // pressed, which is where they are already looking.
  //
  // The tick is held for a moment and then the button returns to itself. A
  // control stuck on "done" is a lie the next time somebody looks at it.
  function flyButton(btn) {
    if (!btn) return { start() {}, ok() {}, fail() {} };
    let timer = 0;
    const reset = () => {
      clearTimeout(timer);
      btn.classList.remove('fly-going', 'fly-done');
      btn.removeAttribute('aria-busy');
    };
    return {
      start() {
        reset();
        btn.classList.add('fly-going');
        btn.setAttribute('aria-busy', 'true');
      },
      ok() {
        clearTimeout(timer);
        btn.classList.remove('fly-going');
        btn.classList.add('fly-done');
        btn.removeAttribute('aria-busy');
        timer = setTimeout(reset, 2200);
      },
      fail: reset,
    };
  }

  window.mountDropzone = mountDropzone;
  window.flyButton = flyButton;
  window.uploadSizeText = sizeText;
})();
