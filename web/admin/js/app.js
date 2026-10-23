/*
 * Admin web: one page, hash routes.
 *   #recap  #transactions  #restock  #products  #machines  #machine-<id>  #operators  #changelog
 */
(function () {
  'use strict';

  var api = window.AdminApi;
  var CONFIG = { adminIdleMs: 30 * 60000, offlineAfterMs: 10 * 60000 };
  var DAY = 86400000;

  /* ================================================================ helpers */

  function Raw(s) { this.s = s; }
  function raw(s) { return new Raw(s); }
  function esc(v) {
    return String(v == null ? '' : v).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function str(v) {
    if (v instanceof Raw) return v.s;
    if (Array.isArray(v)) return v.map(str).join('');
    if (v === false || v == null) return '';
    return esc(v);
  }
  function html(strings) {
    var out = '';
    for (var i = 0; i < strings.length; i++) {
      out += strings[i];
      if (i + 1 < arguments.length) out += str(arguments[i + 1]);
    }
    return raw(out);
  }
  var $ = function (sel, root) { return (root || document).querySelector(sel); };
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

  var rpFmt = new Intl.NumberFormat('id-ID', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 });
  var numFmt = new Intl.NumberFormat('id-ID');
  var rp = function (n) { return rpFmt.format(n || 0); };
  var num = function (n) { return numFmt.format(n || 0); };
  function rpShort(n) {
    if (n >= 1e9) return 'Rp ' + (n / 1e9).toLocaleString('id-ID', { maximumFractionDigits: 1 }) + ' M';
    if (n >= 1e6) return 'Rp ' + (n / 1e6).toLocaleString('id-ID', { maximumFractionDigits: 1 }) + ' jt';
    if (n >= 1e3) return 'Rp ' + Math.round(n / 1e3) + ' rb';
    return 'Rp ' + n;
  }
  var MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'Mei', 'Jun', 'Jul', 'Agu', 'Sep', 'Okt', 'Nov', 'Des'];
  function fmtDate(t) { var d = new Date(t); return d.getDate() + ' ' + MONTHS[d.getMonth()] + ' ' + d.getFullYear(); }
  function fmtTime(t) { var d = new Date(t); return String(d.getHours()).padStart(2, '0') + '.' + String(d.getMinutes()).padStart(2, '0'); }
  function fmtDateTime(t) { return t ? fmtDate(t) + ', ' + fmtTime(t) : '—'; }
  function relTime(t) {
    if (!t) return 'belum pernah';
    var s = Math.round((Date.now() - t) / 1000);
    if (s < 60) return 'baru saja';
    if (s < 3600) return Math.round(s / 60) + ' menit lalu';
    if (s < 86400) return Math.round(s / 3600) + ' jam lalu';
    if (s < 7 * 86400) return Math.round(s / 86400) + ' hari lalu';
    return fmtDate(t);
  }
  function dayKey(t) {
    var d = new Date(t);
    return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
  }
  function parseDay(s) { var p = s.split('-').map(Number); return new Date(p[0], p[1] - 1, p[2]).getTime(); }
  function startOfDay(t) { var d = new Date(t); d.setHours(0, 0, 0, 0); return d.getTime(); }
  function shortId(id) { return String(id).replace(/-/g, '').slice(0, 8).toUpperCase(); }
  function rangeLabel(from, to) {
    return from === to ? fmtDate(parseDay(from)) : fmtDate(parseDay(from)) + ' – ' + fmtDate(parseDay(to));
  }

  var PRESETS = [
    { id: 'today', label: 'Hari ini' },
    { id: '7d', label: '7 hari' },
    { id: '30d', label: '30 hari' },
    { id: 'month', label: 'Bulan ini' },
    { id: '90d', label: '90 hari' }
  ];
  function presetRange(id) {
    var t0 = startOfDay(Date.now());
    var to = dayKey(t0);
    if (id === 'today') return { from: to, to: to };
    if (id === '7d') return { from: dayKey(t0 - 6 * DAY), to: to };
    if (id === 'month') { var d = new Date(t0); return { from: dayKey(new Date(d.getFullYear(), d.getMonth(), 1).getTime()), to: to }; }
    if (id === '90d') return { from: dayKey(t0 - 89 * DAY), to: to };
    return { from: dayKey(t0 - 29 * DAY), to: to };
  }

  var STATUS = {
    DISPENSED: { label: 'Berhasil', cls: 'pill-good' },
    FAILED: { label: 'Gagal keluar', cls: 'pill-bad' },
    CANCELLED: { label: 'Dibatalkan', cls: '' },
    EXPIRED: { label: 'Kedaluwarsa', cls: '' },
    PENDING: { label: 'Menunggu bayar', cls: 'pill-info' },
    PAID: { label: 'Dibayar', cls: 'pill-info' }
  };
  function statusPill(s) { var x = STATUS[s] || { label: s, cls: '' }; return html`<span class="pill ${x.cls}" title="${s}">${x.label}</span>`; }

  function machineState(m) {
    if (!m.isActive) return { label: 'Nonaktif', cls: '' };
    if (!m.lastSeenAt) return { label: 'Belum terhubung', cls: 'pill-warn' };
    if (Date.now() - m.lastSeenAt > CONFIG.offlineAfterMs) return { label: 'Offline', cls: 'pill-bad' };
    return { label: 'Online', cls: 'pill-good' };
  }
  function machinePill(m) { var s = machineState(m); return html`<span class="pill ${s.cls}" title="Kontak terakhir: ${relTime(m.lastSeenAt)}">${s.label}</span>`; }

  var toastTimer;
  function toast(msg, isError) {
    var t = $('#toast');
    t.textContent = msg;
    t.classList.toggle('is-error', !!isError);
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.classList.remove('show'); }, isError ? 5000 : 3200);
  }

  function handleError(err, target) {
    if (err && err.code === 'UNAUTHORIZED') { showLogin('Sesi berakhir. Silakan masuk lagi.'); return; }
    console.error(err);
    var msg = (err && err.message) || 'Terjadi kesalahan.';
    if (target) target.innerHTML = str(html`<div class="error-box">${msg}</div>`);
    else toast(msg, true);
  }

  /* ================================================================ modal */

  function openModal(opts) {
    var root = $('#modal-root');
    var wrap = document.createElement('div');
    wrap.className = 'modal-backdrop';
    wrap.innerHTML = str(html`
      <div class="modal ${opts.wide ? 'modal-wide' : ''}" role="dialog" aria-modal="true" aria-labelledby="modal-title">
        <div class="modal-head"><h2 id="modal-title">${opts.title}</h2><button class="modal-close" type="button" aria-label="Tutup">×</button></div>
        <form class="modal-form" novalidate>
          <div class="modal-body">${opts.body}</div>
          <div class="modal-foot">${(opts.actions || []).map(function (a, i) {
            return html`<button class="btn ${a.kind || ''}" type="${a.submit ? 'submit' : 'button'}" data-i="${i}">${a.label}</button>`;
          })}</div>
        </form>
      </div>`);
    root.appendChild(wrap);
    var prevFocus = document.activeElement;
    var close = function () {
      wrap.remove();
      document.removeEventListener('keydown', onKey);
      if (prevFocus && prevFocus.focus) prevFocus.focus();
    };
    var onKey = function (e) { if (e.key === 'Escape') close(); };
    document.addEventListener('keydown', onKey);
    wrap.addEventListener('mousedown', function (e) { if (e.target === wrap) close(); });
    $('.modal-close', wrap).addEventListener('click', close);
    var form = $('.modal-form', wrap);
    var run = async function (a, btn) {
      if (!a.onClick) return close();
      btn.disabled = true;
      try { var keep = await a.onClick(form, close); if (keep !== false) close(); }
      catch (err) { handleModalError(form, err); }
      finally { if (document.body.contains(btn)) btn.disabled = false; }
    };
    (opts.actions || []).forEach(function (a, i) {
      var btn = $('[data-i="' + i + '"]', wrap);
      if (!a.submit) btn.addEventListener('click', function () { run(a, btn); });
    });
    form.addEventListener('submit', function (e) {
      e.preventDefault();
      var i = (opts.actions || []).findIndex(function (a) { return a.submit; });
      if (i >= 0) run(opts.actions[i], $('[data-i="' + i + '"]', wrap));
    });
    if (opts.onMount) opts.onMount(form, close);
    var first = $('input:not([type="hidden"]), select, textarea', wrap) || $('.modal-foot .btn', wrap);
    if (first) setTimeout(function () { first.focus(); }, 30);
    return close;
  }

  function handleModalError(form, err) {
    $$('[aria-invalid]', form).forEach(function (el) { el.removeAttribute('aria-invalid'); });
    $$('.field-error', form).forEach(function (el) { el.textContent = ''; });
    if (err && err.code === 'VALIDATION' && err.data.fields) {
      Object.keys(err.data.fields).forEach(function (k) {
        var input = form.elements[k];
        if (input) input.setAttribute('aria-invalid', 'true');
        var slot = $('[data-error="' + k + '"]', form);
        if (slot) slot.textContent = err.data.fields[k];
      });
    }
    var box = $('.form-error', form);
    var msg = (err && err.message) || 'Terjadi kesalahan.';
    if (box) box.textContent = msg; else toast(msg, true);
    if (err && err.code === 'UNAUTHORIZED') showLogin('Sesi berakhir. Silakan masuk lagi.');
  }

  function confirmModal(opts) {
    return openModal({
      title: opts.title,
      body: html`<p>${opts.text}</p><p class="form-error" role="alert"></p>`,
      actions: [{ label: 'Batal' }, { label: opts.confirmLabel, kind: opts.danger ? 'btn-danger solid' : 'btn-primary', submit: true, onClick: opts.onConfirm }]
    });
  }

  /* ================================================================ CSV export */

  function toCsv(rows) {
    return rows.map(function (r) {
      return r.map(function (v) {
        var s = v == null ? '' : String(v);
        return /[",\n;]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
      }).join(',');
    }).join('\n');
  }

  function exportCsv(filename, rows) {
    var csv = toCsv(rows);
    openModal({
      title: 'Ekspor CSV',
      wide: true,
      body: html`<p class="muted">${filename} · ${num(rows.length - 1)} baris</p><pre class="csv-preview">${csv.split('\n').slice(0, 40).join('\n')}${rows.length > 41 ? '\n…' : ''}</pre><p class="form-error" role="alert"></p>`,
      actions: [
        { label: 'Salin', onClick: function (form) {
          return navigator.clipboard.writeText(csv).then(function () { toast('CSV disalin.'); return true; }, function () {
            $('.form-error', form).textContent = 'Browser menolak menyalin. Pilih teks di atas lalu salin manual.';
            return false;
          });
        } },
        (window.ADMIN_CONFIG || {}).downloads === false ? null : { label: 'Unduh CSV', kind: 'btn-primary', onClick: function () {
          var blob = new Blob(['﻿' + csv], { type: 'text/csv;charset=utf-8' });
          var a = document.createElement('a');
          a.href = URL.createObjectURL(blob);
          a.download = filename;
          document.body.appendChild(a);
          a.click();
          setTimeout(function () { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
        } }
      ].filter(Boolean)
    });
  }

  /* ================================================================ state, auth, routing */

  var state = {
    user: null,
    machines: [],
    route: 'recap',
    recap: { preset: '30d', from: null, to: null, groupBy: 'day', machineId: '', breakdown: 'product' },
    tx: { preset: '7d', from: null, to: null, machineId: '', slotNumber: '', status: '', flag: '', page: 1 },
    restock: { preset: '7d', from: null, to: null, machineId: '', userId: '', page: 1, open: {} },
    products: { showArchived: false, q: '' },
    changelog: { preset: '90d', entityType: '' }
  };
  ['recap', 'tx', 'restock'].forEach(function (k) { Object.assign(state[k], presetRange(state[k].preset)); });

  var idleTimer;
  function resetIdle() {
    clearTimeout(idleTimer);
    if (state.user) idleTimer = setTimeout(function () { logout('Anda keluar otomatis setelah 30 menit tanpa aktivitas.'); }, CONFIG.adminIdleMs);
  }
  ['pointerdown', 'keydown'].forEach(function (ev) { document.addEventListener(ev, resetIdle, true); });

  function showLogin(message) {
    state.user = null;
    $('#app-view').hidden = true;
    $('#login-view').hidden = false;
    $('#login-error').textContent = message || '';
    $('#login-demo').hidden = !api.isMock;
    if (api.isMock && !$('#login-username').value) { $('#login-username').value = 'admin'; $('#login-password').value = 'admin123'; }
    setTimeout(function () { $('#login-username').focus(); }, 30);
  }

  async function enterApp(user) {
    if (user.demoMode === undefined && !api.isMock) {
      try { user = Object.assign({}, user, await api.me()); } catch (_) { /* keep what the login returned */ }
    }
    state.user = user;
    $('#demo-reset').hidden = !user.demoMode;
    $('#login-view').hidden = true;
    $('#app-view').hidden = false;
    $('#nav-user').textContent = user.name;
    $('#demo-banner').hidden = !api.isMock;
    resetIdle();
    await refreshMachines();
    route();
  }

  async function refreshMachines() {
    try { state.machines = await api.getMachines(); } catch (err) { handleError(err); }
  }

  async function logout(message) {
    try { await api.logout(); } catch (_) { /* already gone */ }
    clearTimeout(idleTimer);
    showLogin(message);
  }

  $('#login-form').addEventListener('submit', async function (e) {
    e.preventDefault();
    var btn = $('#login-submit');
    btn.disabled = true;
    $('#login-error').textContent = '';
    try {
      var r = await api.login($('#login-username').value, $('#login-password').value);
      $('#login-password').value = '';
      await enterApp({ name: r.name, username: r.username });
    } catch (err) {
      $('#login-error').textContent = err.code === 'OFFLINE' ? 'Server tidak terjangkau.' : (err.message || 'Gagal masuk.');
    } finally {
      btn.disabled = false;
    }
  });
  $('#logout').addEventListener('click', function () { logout(); });
  $('#demo-reset').addEventListener('click', function () {
    confirmModal({
      title: 'Reset semua data demo?', confirmLabel: 'Reset data', danger: true,
      text: 'Semua transaksi, restock, produk, mesin dan akun dihapus lalu diisi ulang dengan data contoh. Kunci mesin VM-01 sampai VM-03 tetap sama. Anda akan keluar dan masuk lagi dengan admin / admin123.',
      onConfirm: async function () {
        await api.resetDemo();
        clearTimeout(idleTimer);
        setTimeout(function () { showLogin('Data demo sudah direset. Masuk dengan admin / admin123.'); }, 50);
      }
    });
  });

  function go(r) {
    if (('#' + r) !== location.hash) {
      try { location.hash = r; } catch (_) { /* frame may refuse; render anyway */ }
    }
    route(r);
  }
  window.addEventListener('hashchange', function () { if (state.user) route(); });

  var VIEWS = {};
  var renderToken = 0;

  function route(forced) {
    var r = forced || (location.hash || '#recap').slice(1) || 'recap';
    var m = /^machine-(\d+)$/.exec(r);
    var name = m ? 'machine' : VIEWS[r] ? r : 'recap';
    state.route = r;
    $$('#nav-links a').forEach(function (a) {
      var active = a.dataset.route === name || (name === 'machine' && a.dataset.route === 'machines');
      a.classList.toggle('active', active);
      if (active) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    var view = $('#view');
    var my = ++renderToken;
    view.innerHTML = '<div class="loading">Memuat…</div>';
    Promise.resolve(VIEWS[name](view, m ? Number(m[1]) : null, function () { return my === renderToken; }))
      .catch(function (err) { if (my === renderToken) handleError(err, view); });
    window.scrollTo(0, 0);
  }

  $('#nav-links').addEventListener('click', function (e) {
    var a = e.target.closest('a[data-route]');
    if (!a) return;
    e.preventDefault();
    go(a.dataset.route);
  });

  /* ================================================================ shared view pieces */

  function machineOptions(selected, allLabel) {
    return html`<option value="">${allLabel || 'Semua mesin'}</option>${state.machines.map(function (m) {
      return html`<option value="${m.id}" ${String(selected) === String(m.id) ? raw('selected') : ''}>${m.code} · ${m.name}</option>`;
    })}`;
  }

  function presetSeg(current, name) {
    return html`<div class="seg" role="group" aria-label="Periode" data-seg="${name}">${PRESETS.map(function (p) {
      return html`<button type="button" data-preset="${p.id}" aria-pressed="${current === p.id}">${p.label}</button>`;
    })}</div>`;
  }

  function dateFields(f, prefix) {
    return html`
      <div class="field field-sm"><label class="label" for="${prefix}-from">Dari</label><input class="input" type="date" id="${prefix}-from" value="${f.from}" max="${f.to}"></div>
      <div class="field field-sm"><label class="label" for="${prefix}-to">Sampai</label><input class="input" type="date" id="${prefix}-to" value="${f.to}" min="${f.from}" max="${dayKey(Date.now())}"></div>`;
  }

  function bindPeriod(root, f, prefix, rerender) {
    $$('[data-preset]', root).forEach(function (b) {
      b.addEventListener('click', function () { Object.assign(f, presetRange(b.dataset.preset), { preset: b.dataset.preset, page: 1 }); rerender(); });
    });
    var from = $('#' + prefix + '-from', root), to = $('#' + prefix + '-to', root);
    var onDate = function () {
      if (!from.value || !to.value) return;
      if (from.value > to.value) { toast('Tanggal "dari" harus sebelum "sampai".', true); return; }
      Object.assign(f, { from: from.value, to: to.value, preset: 'custom', page: 1 });
      rerender();
    };
    from.addEventListener('change', onDate);
    to.addEventListener('change', onDate);
  }

  function pager(total, page, pageSize) {
    var pages = Math.max(1, Math.ceil(total / pageSize));
    var a = total ? (page - 1) * pageSize + 1 : 0, b = Math.min(total, page * pageSize);
    return html`<span>${num(a)}–${num(b)} dari ${num(total)}</span>
      <span class="pager"><button class="btn btn-sm" type="button" data-page="${page - 1}" ${page <= 1 ? raw('disabled') : ''}>Sebelumnya</button>
      <span class="muted">Hal. ${page} / ${pages}</span>
      <button class="btn btn-sm" type="button" data-page="${page + 1}" ${page >= pages ? raw('disabled') : ''}>Berikutnya</button></span>`;
  }

  function share(value, total) {
    var pct = total ? value / total * 100 : 0;
    return html`<div class="share"><div class="share-track"><div class="share-fill" style="width:${pct.toFixed(1)}%"></div></div><span class="share-pct">${pct < 1 && pct > 0 ? '<1' : Math.round(pct)}%</span></div>`;
  }

  /* ================================================================ revenue chart */

  function niceStep(max, ticks) {
    var raw = max / ticks;
    var mag = Math.pow(10, Math.floor(Math.log10(raw || 1)));
    var n = raw / mag;
    return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * mag;
  }

  function periodLabel(p, groupBy, long) {
    if (groupBy === 'month') { var y = p.period.split('-'); return MONTHS[Number(y[1]) - 1] + ' ' + (long ? y[0] : y[0].slice(2)); }
    var d = new Date(p.start);
    var s = d.getDate() + ' ' + MONTHS[d.getMonth()];
    if (groupBy === 'week') return long ? 'Minggu ' + s + ' – ' + (function () { var e = new Date(p.start + 6 * DAY); return e.getDate() + ' ' + MONTHS[e.getMonth()]; })() : s;
    return long ? ['Min', 'Sen', 'Sel', 'Rab', 'Kam', 'Jum', 'Sab'][d.getDay()] + ', ' + s + ' ' + d.getFullYear() : s;
  }

  function drawRevenueChart(box, series, groupBy) {
    var width = Math.max(280, box.clientWidth - 16);
    var height = width < 520 ? 210 : 250;
    var m = { top: 12, right: 8, bottom: 26, left: 62 };
    var iw = width - m.left - m.right, ih = height - m.top - m.bottom;
    var max = Math.max.apply(null, series.map(function (s) { return s.revenue; }).concat([0]));
    if (!series.length || max === 0) { box.innerHTML = '<div class="chart-empty">Belum ada penjualan berhasil di periode ini.</div>'; return; }
    var step = niceStep(max, 4), top = Math.ceil(max / step) * step;
    var y = function (v) { return m.top + ih - v / top * ih; };
    var band = iw / series.length;
    var gap = Math.max(2, band * 0.25);
    var bw = Math.max(1, Math.min(34, band - gap));
    var every = Math.max(1, Math.ceil(series.length / Math.max(2, Math.floor(iw / 64))));

    var grid = '', axis = '', bars = '';
    for (var v = 0; v <= top + 1e-9; v += step) {
      grid += '<line x1="' + m.left + '" x2="' + (width - m.right) + '" y1="' + y(v).toFixed(1) + '" y2="' + y(v).toFixed(1) + '"/>';
      axis += '<text x="' + (m.left - 8) + '" y="' + (y(v) + 4).toFixed(1) + '" text-anchor="end">' + esc(v === 0 ? '0' : rpShort(v)) + '</text>';
    }
    series.forEach(function (s, i) {
      var cx = m.left + band * i + band / 2;
      var h = s.revenue / top * ih;
      var x = cx - bw / 2;
      bars += '<rect class="hit" data-i="' + i + '" x="' + (m.left + band * i).toFixed(1) + '" y="' + m.top + '" width="' + band.toFixed(1) + '" height="' + ih + '" fill="transparent"/>';
      if (h > 0) {
        var r = Math.min(4, bw / 2, h);
        var yt = m.top + ih - h, yb = m.top + ih;
        bars += '<path class="bar" data-i="' + i + '" d="M' + x.toFixed(1) + ',' + yb + 'V' + (yt + r).toFixed(1) +
          'Q' + x.toFixed(1) + ',' + yt.toFixed(1) + ' ' + (x + r).toFixed(1) + ',' + yt.toFixed(1) +
          'H' + (x + bw - r).toFixed(1) + 'Q' + (x + bw).toFixed(1) + ',' + yt.toFixed(1) + ' ' + (x + bw).toFixed(1) + ',' + (yt + r).toFixed(1) +
          'V' + yb + 'Z"/>';
      }
      if (i % every === 0) axis += '<text x="' + cx.toFixed(1) + '" y="' + (height - 6) + '" text-anchor="middle">' + esc(periodLabel(s, groupBy)) + '</text>';
    });
    box.innerHTML = '<svg viewBox="0 0 ' + width + ' ' + height + '" height="' + height + '" role="img" aria-label="Pendapatan per periode">' +
      '<g class="grid">' + grid + '</g><g class="axis">' + axis + '</g><g>' + bars + '</g></svg>';

    var tip = $('#chart-tip');
    var svg = $('svg', box);
    var hover = function (e) {
      var hit = e.target.closest('.hit');
      $$('.bar.is-hover', svg).forEach(function (b) { b.classList.remove('is-hover'); });
      if (!hit) { tip.hidden = true; return; }
      var i = Number(hit.dataset.i), s = series[i];
      var bar = $('.bar[data-i="' + i + '"]', svg);
      if (bar) bar.classList.add('is-hover');
      tip.innerHTML = str(html`${periodLabel(s, groupBy, true)}<br><b>${rp(s.revenue)}</b> · ${num(s.sales)} penjualan`);
      tip.hidden = false;
      var tx = Math.min(window.innerWidth - tip.offsetWidth - 8, Math.max(8, e.clientX - tip.offsetWidth / 2));
      tip.style.left = tx + 'px';
      tip.style.top = (e.clientY - tip.offsetHeight - 14) + 'px';
    };
    svg.addEventListener('pointermove', hover);
    svg.addEventListener('pointerdown', hover);
    svg.addEventListener('pointerleave', function () { tip.hidden = true; $$('.bar.is-hover', svg).forEach(function (b) { b.classList.remove('is-hover'); }); });
  }

  var resizeHandlers = [];
  var resizeTimer;
  window.addEventListener('resize', function () {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(function () { resizeHandlers.forEach(function (fn) { fn(); }); }, 150);
  });
  function onResize(fn) { resizeHandlers = [fn]; }

  /* ================================================================ 1. revenue recap */

  VIEWS.recap = async function (view, _id, alive) {
    var f = state.recap;
    onResize(null);
    resizeHandlers = [];
    var params = { from: f.from, to: f.to, machineId: f.machineId, groupBy: f.groupBy, breakdown: f.breakdown };
    var results = await Promise.all([api.getSummary({ machineId: f.machineId }), api.getRecap(params)]);
    if (!alive()) return;
    var sm = results[0], rc = results[1];
    var t = rc.totals;
    var totalRevenue = t.revenue;
    var dToday = sm.sameTimeYesterday.revenue ? (sm.today.revenue - sm.sameTimeYesterday.revenue) / sm.sameTimeYesterday.revenue * 100 : null;
    var machineLabel = f.machineId ? (state.machines.find(function (m) { return String(m.id) === String(f.machineId); }) || {}).code : 'Semua mesin';
    var review = t.failed + t.reported;

    view.innerHTML = str(html`
      <header class="page-head">
        <div><h1>Rekap pendapatan</h1><p class="sub">${machineLabel} · hanya transaksi yang berhasil keluar dihitung sebagai pendapatan</p></div>
        <div class="head-actions">
          <div class="field"><label class="label" for="rc-machine">Mesin</label><select class="select" id="rc-machine">${machineOptions(f.machineId)}</select></div>
        </div>
      </header>

      <section class="tiles" aria-label="Ringkasan">
        <div class="card tile"><span class="tile-label">Hari ini</span><span class="tile-value">${rp(sm.today.revenue)}</span>
          <span class="tile-meta">${num(sm.today.sales)} penjualan${dToday === null ? '' : html` · <span class="${dToday >= 0 ? 'delta-up' : 'delta-down'}">${dToday >= 0 ? '+' : '−'}${Math.abs(Math.round(dToday))}%</span> dari kemarin di jam yang sama`}</span></div>
        <div class="card tile"><span class="tile-label">7 hari terakhir</span><span class="tile-value">${rp(sm.last7.revenue)}</span><span class="tile-meta">${num(sm.last7.sales)} penjualan</span></div>
        <div class="card tile"><span class="tile-label">Bulan ini</span><span class="tile-value">${rp(sm.month.revenue)}</span><span class="tile-meta">${num(sm.month.sales)} penjualan</span></div>
      </section>

      <section class="card">
        <div class="card-head">
          <h2>Pendapatan · ${rangeLabel(rc.from, rc.to)}</h2>
          <div class="filters">
            ${presetSeg(f.preset, 'recap')}
            ${dateFields(f, 'rc')}
            <div class="field field-sm"><label class="label" for="rc-group">Per</label>
              <select class="select" id="rc-group">
                <option value="day" ${f.groupBy === 'day' ? raw('selected') : ''}>Hari</option>
                <option value="week" ${f.groupBy === 'week' ? raw('selected') : ''}>Minggu</option>
                <option value="month" ${f.groupBy === 'month' ? raw('selected') : ''}>Bulan</option>
              </select></div>
            <button class="btn" type="button" id="rc-export">Ekspor CSV</button>
          </div>
        </div>
        <div class="chart-totals">
          <span class="chart-total">${rp(totalRevenue)}</span>
          <span class="chart-stat"><b>${num(t.sales)}</b> penjualan</span>
          <span class="chart-stat">rata-rata <b>${rp(t.sales ? Math.round(totalRevenue / t.sales) : 0)}</b></span>
          <span class="chart-stat"><b>${num(t.cancelled)}</b> batal atau kedaluwarsa</span>
          ${review ? html`<button class="link-btn" type="button" id="rc-review">${num(review)} perlu ditinjau (${num(t.failed)} gagal keluar, ${num(t.reported)} dilaporkan pelanggan)</button>` : html`<span class="chart-stat">Tidak ada yang perlu ditinjau</span>`}
        </div>
        <div class="chart-box" id="rc-chart"></div>
      </section>

      <div class="grid-2">
        <section class="card">
          <div class="card-head"><h2>Per mesin</h2></div>
          <div class="table-wrap"><table class="table">
            <thead><tr><th>Mesin</th><th>Status</th><th class="num">Penjualan</th><th class="num">Pendapatan</th><th>Porsi</th><th>Penjualan terakhir</th></tr></thead>
            <tbody>${rc.byMachine.length ? rc.byMachine.map(function (m) {
              return html`<tr>
                <td><button class="link-btn" type="button" data-machine="${m.machineId}"><span class="mono">${m.code}</span></button><div class="sub">${m.name}</div></td>
                <td>${machinePill(m)}</td>
                <td class="num">${num(m.sales)}</td>
                <td class="num">${rp(m.revenue)}</td>
                <td>${share(m.revenue, totalRevenue)}</td>
                <td class="nowrap">${relTime(m.lastSaleAt)}</td></tr>`;
            }) : html`<tr class="empty-row"><td colspan="6">Belum ada mesin.</td></tr>`}</tbody>
          </table></div>
        </section>
        <section class="card">
          <div class="card-head"><h2>5 produk teratas</h2></div>
          ${rc.topProducts.length ? html`<ol class="rank-list">${rc.topProducts.map(function (p) {
            return html`<li class="rank-row"><span class="rank-name">${p.label}</span><span class="rank-value">${rp(p.revenue)}</span>
              <span class="share-track"><span class="share-fill" style="display:block;width:${(p.revenue / rc.topProducts[0].revenue * 100).toFixed(1)}%"></span></span>
              <span class="rank-sub">${num(p.sales)} terjual · ${Math.round(p.revenue / (totalRevenue || 1) * 100)}% dari pendapatan</span></li>`;
          })}</ol>` : html`<p class="card-body muted">Belum ada penjualan.</p>`}
        </section>
      </div>

      <section class="card">
        <div class="card-head"><h2>Rincian per ${f.breakdown === 'slot' ? 'slot' : 'produk'}</h2>
          <div class="seg" role="group" aria-label="Rincian">
            <button type="button" data-breakdown="product" aria-pressed="${f.breakdown !== 'slot'}">Produk</button>
            <button type="button" data-breakdown="slot" aria-pressed="${f.breakdown === 'slot'}">Slot</button>
          </div></div>
        <div class="table-wrap"><table class="table">
          <thead><tr><th>${f.breakdown === 'slot' ? 'Slot' : 'Produk'}</th><th class="num">Terjual</th><th class="num">Pendapatan</th><th>Porsi</th></tr></thead>
          <tbody>${rc.breakdown.length ? rc.breakdown.map(function (b) {
            return html`<tr><td>${b.label}</td><td class="num">${num(b.sales)}</td><td class="num">${rp(b.revenue)}</td><td>${share(b.revenue, totalRevenue)}</td></tr>`;
          }) : html`<tr class="empty-row"><td colspan="4">Belum ada penjualan di periode ini.</td></tr>`}</tbody>
        </table></div>
      </section>`);

    var chartBox = $('#rc-chart', view);
    drawRevenueChart(chartBox, rc.series, rc.groupBy);
    onResize(function () { if (document.body.contains(chartBox)) drawRevenueChart(chartBox, rc.series, rc.groupBy); });

    var rerender = function () { route('recap'); };
    bindPeriod(view, f, 'rc', rerender);
    $('#rc-machine', view).addEventListener('change', function () { f.machineId = this.value; rerender(); });
    $('#rc-group', view).addEventListener('change', function () { f.groupBy = this.value; rerender(); });
    $$('[data-breakdown]', view).forEach(function (b) { b.addEventListener('click', function () { f.breakdown = b.dataset.breakdown; rerender(); }); });
    $$('[data-machine]', view).forEach(function (b) { b.addEventListener('click', function () { go('machine-' + b.dataset.machine); }); });
    var rev = $('#rc-review', view);
    if (rev) rev.addEventListener('click', function () {
      Object.assign(state.tx, { from: f.from, to: f.to, preset: f.preset, machineId: f.machineId, status: '', flag: 'problem', page: 1 });
      go('transactions');
    });
    $('#rc-export', view).addEventListener('click', function () {
      var rows = [['periode', 'pendapatan_rp', 'penjualan']].concat(rc.series.map(function (s) { return [s.period, s.revenue, s.sales]; }));
      rows.push([]);
      rows.push([f.breakdown === 'slot' ? 'slot' : 'produk', 'pendapatan_rp', 'terjual']);
      rc.breakdown.forEach(function (b) { rows.push([b.label, b.revenue, b.sales]); });
      exportCsv('rekap-' + rc.from + '_' + rc.to + '.csv', rows);
    });
  };

  /* ================================================================ 2. transactions */

  VIEWS.transactions = async function (view, _id, alive) {
    var f = state.tx;
    var params = { from: f.from, to: f.to, machineId: f.machineId, slotNumber: f.slotNumber, status: f.status, flag: f.flag, page: f.page, pageSize: 25 };
    var r = await api.getTransactions(params);
    if (!alive()) return;
    var machine = state.machines.find(function (m) { return String(m.id) === String(f.machineId); });

    view.innerHTML = str(html`
      <header class="page-head">
        <div><h1>Transaksi</h1><p class="sub">Setiap percobaan pembelian, termasuk yang dibatalkan dan gagal keluar</p></div>
        <div class="head-actions"><button class="btn" type="button" id="tx-export">Ekspor CSV</button></div>
      </header>
      <div class="filters">
        ${presetSeg(f.preset, 'tx')}
        ${dateFields(f, 'tx')}
        <div class="field"><label class="label" for="tx-machine">Mesin</label><select class="select" id="tx-machine">${machineOptions(f.machineId)}</select></div>
        <div class="field field-sm"><label class="label" for="tx-slot">Slot</label>
          <select class="select" id="tx-slot" ${machine ? '' : raw('disabled')}><option value="">Semua</option>${machine ? Array.from({ length: machine.slotCount }, function (_, i) {
            return html`<option value="${i + 1}" ${String(f.slotNumber) === String(i + 1) ? raw('selected') : ''}>Slot ${i + 1}</option>`;
          }) : ''}</select></div>
        <div class="field"><label class="label" for="tx-status">Status</label>
          <select class="select" id="tx-status"><option value="">Semua status</option>${Object.keys(STATUS).map(function (k) {
            return html`<option value="${k}" ${f.status === k ? raw('selected') : ''}>${STATUS[k].label}</option>`;
          })}</select></div>
        <div class="field"><label class="label" for="tx-flag">Tinjauan</label>
          <select class="select" id="tx-flag">
            <option value="">Semua transaksi</option>
            <option value="problem" ${f.flag === 'problem' ? raw('selected') : ''}>Gagal keluar atau dilaporkan</option>
            <option value="reported" ${f.flag === 'reported' ? raw('selected') : ''}>Dilaporkan pelanggan</option>
          </select></div>
      </div>
      <section class="card">
        <div class="card-head"><h2>${num(r.total)} transaksi · ${rangeLabel(f.from, f.to)}</h2>
          <span class="chart-stat">Pendapatan <b>${rp(r.dispensedRevenue)}</b> dari ${num(r.dispensedCount)} yang berhasil</span></div>
        <div class="table-wrap"><table class="table">
          <thead><tr><th>Waktu</th><th>ID</th><th>Mesin</th><th class="num">Slot</th><th>Produk</th><th class="num">Harga</th><th>Status</th><th>Catatan</th></tr></thead>
          <tbody>${r.rows.length ? r.rows.map(function (t) {
            var note = [];
            if (t.failureReason) note.push(html`<span class="tag tag-bad">${t.failureReason}</span>`);
            if (t.customerReportedAt) note.push(html`<span class="tag tag-bad" title="${fmtDateTime(t.customerReportedAt)}">Dilaporkan: produk tidak keluar</span>`);
            return html`<tr>
              <td class="nowrap">${fmtDateTime(t.createdAt)}</td>
              <td><span class="mono" title="${t.id}">${shortId(t.id)}</span></td>
              <td><span class="mono">${t.machine.code}</span></td>
              <td class="num">${t.slotNumber}</td>
              <td>${t.product.name}</td>
              <td class="num">${rp(t.price)}</td>
              <td>${statusPill(t.status)}</td>
              <td>${note.length ? note : html`<span class="muted">—</span>`}</td></tr>`;
          }) : html`<tr class="empty-row"><td colspan="8">Tidak ada transaksi yang cocok dengan filter ini.</td></tr>`}</tbody>
        </table></div>
        <div class="card-foot">${pager(r.total, r.page, r.pageSize)}</div>
      </section>`);

    var rerender = function () { route('transactions'); };
    bindPeriod(view, f, 'tx', rerender);
    $('#tx-machine', view).addEventListener('change', function () { f.machineId = this.value; f.slotNumber = ''; f.page = 1; rerender(); });
    $('#tx-slot', view).addEventListener('change', function () { f.slotNumber = this.value; f.page = 1; rerender(); });
    $('#tx-status', view).addEventListener('change', function () { f.status = this.value; f.page = 1; rerender(); });
    $('#tx-flag', view).addEventListener('change', function () { f.flag = this.value; f.page = 1; rerender(); });
    $$('[data-page]', view).forEach(function (b) { b.addEventListener('click', function () { f.page = Number(b.dataset.page); rerender(); }); });
    $('#tx-export', view).addEventListener('click', async function () {
      try {
        var all = await api.getTransactions(Object.assign({}, params, { all: 1 }));
        var rows = [['waktu', 'id', 'mesin', 'slot', 'produk', 'harga_rp', 'status', 'alasan_gagal', 'dilaporkan_pelanggan']].concat(all.rows.map(function (t) {
          return [new Date(t.createdAt).toISOString(), t.id, t.machine.code, t.slotNumber, t.product.name, t.price, t.status, t.failureReason || '', t.customerReportedAt ? new Date(t.customerReportedAt).toISOString() : ''];
        }));
        exportCsv('transaksi-' + f.from + '_' + f.to + '.csv', rows);
      } catch (err) { handleError(err); }
    });
  };

  /* ================================================================ 3. restock history */

  VIEWS.restock = async function (view, _id, alive) {
    var f = state.restock;
    var results = await Promise.all([
      api.getRestockSessions({ from: f.from, to: f.to, machineId: f.machineId, userId: f.userId, page: f.page, pageSize: 20 }),
      api.getOperators()
    ]);
    if (!alive()) return;
    var r = results[0], ops = results[1];

    view.innerHTML = str(html`
      <header class="page-head"><div><h1>Riwayat restock</h1><p class="sub">Satu baris per kunjungan operator; buka untuk melihat isi tiap slot</p></div></header>
      <div class="filters">
        ${presetSeg(f.preset, 'restock')}
        ${dateFields(f, 'rs')}
        <div class="field"><label class="label" for="rs-machine">Mesin</label><select class="select" id="rs-machine">${machineOptions(f.machineId)}</select></div>
        <div class="field"><label class="label" for="rs-user">Operator</label><select class="select" id="rs-user"><option value="">Semua operator</option>${ops.map(function (o) {
          return html`<option value="${o.id}" ${String(f.userId) === String(o.id) ? raw('selected') : ''}>${o.name}</option>`;
        })}</select></div>
      </div>
      <section class="card">
        <div class="card-head"><h2>${num(r.total)} kunjungan · ${rangeLabel(f.from, f.to)}</h2></div>
        <div class="table-wrap"><table class="table">
          <thead><tr><th>Mulai</th><th>Mesin</th><th>Operator</th><th class="num">Durasi</th><th class="num">Slot berubah</th><th class="num">Item masuk</th><th></th></tr></thead>
          <tbody>${r.rows.length ? r.rows.map(function (s) {
            var open = !!f.open[s.id];
            return html`<tr>
              <td class="nowrap">${fmtDateTime(s.startedAt)}</td>
              <td><span class="mono">${s.machine.code}</span> <span class="sub">${s.machine.name}</span></td>
              <td>${s.operator.name}</td>
              <td class="num">${Math.round((s.finishedAt - s.startedAt) / 60000)} mnt</td>
              <td class="num">${s.slotsChanged}</td>
              <td class="num">+${num(s.itemsAdded)}</td>
              <td><button class="btn btn-sm" type="button" data-open="${s.id}" aria-expanded="${open}">${open ? 'Tutup' : 'Rincian'}</button></td></tr>
              ${open ? html`<tr class="detail-row"><td colspan="7"><table class="inner-table">
                <thead><tr><th>Slot</th><th>Produk</th><th class="num">Sebelum</th><th class="num">Sesudah</th><th class="num">Selisih</th></tr></thead>
                <tbody>${s.items.map(function (it) {
                  var d = it.qtyAfter - it.qtyBefore;
                  return html`<tr><td>${it.slotNumber}</td><td>${it.productName} ${it.productChanged ? html`<span class="tag">Ganti produk</span>` : ''}</td>
                    <td class="num">${it.qtyBefore}</td><td class="num">${it.qtyAfter}</td>
                    <td class="num ${d > 0 ? 'diff-pos' : d < 0 ? 'diff-neg' : 'muted'}">${d > 0 ? '+' + d : d < 0 ? '−' + Math.abs(d) : 'tetap'}</td></tr>`;
                })}</tbody></table></td></tr>` : ''}`;
          }) : html`<tr class="empty-row"><td colspan="7">Belum ada kunjungan restock di periode ini.</td></tr>`}</tbody>
        </table></div>
        <div class="card-foot">${pager(r.total, r.page, r.pageSize)}</div>
      </section>`);

    var rerender = function () { route('restock'); };
    bindPeriod(view, f, 'rs', rerender);
    $('#rs-machine', view).addEventListener('change', function () { f.machineId = this.value; f.page = 1; rerender(); });
    $('#rs-user', view).addEventListener('change', function () { f.userId = this.value; f.page = 1; rerender(); });
    $$('[data-open]', view).forEach(function (b) { b.addEventListener('click', function () { f.open[b.dataset.open] = !f.open[b.dataset.open]; rerender(); }); });
    $$('[data-page]', view).forEach(function (b) { b.addEventListener('click', function () { f.page = Number(b.dataset.page); rerender(); }); });
  };

  /* ================================================================ 4. products */

  VIEWS.products = async function (view, _id, alive) {
    var f = state.products;
    var list = await api.getProducts({ includeArchived: true });
    if (!alive()) return;
    var q = f.q.trim().toLowerCase();
    var rows = list.filter(function (p) { return (f.showArchived || p.isActive) && (!q || p.name.toLowerCase().indexOf(q) >= 0); });
    var archivedCount = list.filter(function (p) { return !p.isActive; }).length;

    view.innerHTML = str(html`
      <header class="page-head">
        <div><h1>Produk</h1><p class="sub">Satu harga untuk semua mesin. Perubahan muncul di kiosk saat pembelian berikutnya atau saat kiosk memuat ulang katalog.</p></div>
        <div class="head-actions"><button class="btn btn-primary" type="button" id="pr-add">Tambah produk</button></div>
      </header>
      <div class="filters">
        <div class="field"><label class="label" for="pr-q">Cari</label><input class="input" id="pr-q" type="search" placeholder="Nama produk" value="${f.q}"></div>
        <label class="check" for="pr-arch"><input type="checkbox" id="pr-arch" ${f.showArchived ? raw('checked') : ''}> Tampilkan arsip (${archivedCount})</label>
      </div>
      <section class="card"><div class="table-wrap"><table class="table">
        <thead><tr><th></th><th>Produk</th><th class="num">Harga</th><th>Dipakai di</th><th>Diubah</th><th>Status</th><th></th></tr></thead>
        <tbody>${rows.length ? rows.map(function (p) {
          return html`<tr>
            <td><img class="thumb" src="${p.imageUrl || ''}" alt=""></td>
            <td><b>${p.name}</b><div class="sub">${p.description || '—'}</div></td>
            <td class="num">${rp(p.price)}</td>
            <td>${p.usedIn.length ? p.usedIn.map(function (u) { return html`<span class="tag" style="margin:0 4px 4px 0">${u.machineCode} #${u.slotNumber}${u.planned ? ' (rencana)' : ''}</span>`; }) : html`<span class="muted">Tidak di slot mana pun</span>`}</td>
            <td class="nowrap">${relTime(p.updatedAt)}</td>
            <td>${p.isActive ? html`<span class="pill pill-good">Aktif</span>` : html`<span class="pill">Arsip</span>`}</td>
            <td><div class="actions">${p.isActive
              ? html`<button class="btn btn-sm" type="button" data-edit="${p.id}">Ubah</button><button class="btn btn-sm btn-danger" type="button" data-archive="${p.id}">Arsipkan</button>`
              : html`<button class="btn btn-sm" type="button" data-restore="${p.id}">Pulihkan</button>`}</div></td></tr>`;
        }) : html`<tr class="empty-row"><td colspan="7">Tidak ada produk yang cocok.</td></tr>`}</tbody>
      </table></div></section>`);

    var rerender = function () { route('products'); };
    var qInput = $('#pr-q', view);
    qInput.addEventListener('input', function () {
      f.q = this.value;
      clearTimeout(qInput._t);
      qInput._t = setTimeout(function () {
        var pos = f.q.length;
        rerender();
        setTimeout(function () { var n = $('#pr-q'); if (n) { n.focus(); n.setSelectionRange(pos, pos); } }, 260);
      }, 250);
    });
    $('#pr-arch', view).addEventListener('change', function () { f.showArchived = this.checked; rerender(); });
    $('#pr-add', view).addEventListener('click', function () { productForm(null, rerender); });
    $$('[data-edit]', view).forEach(function (b) {
      b.addEventListener('click', function () { productForm(list.find(function (p) { return String(p.id) === b.dataset.edit; }), rerender); });
    });
    $$('[data-archive]', view).forEach(function (b) {
      b.addEventListener('click', function () {
        var p = list.find(function (x) { return String(x.id) === b.dataset.archive; });
        if (p.usedIn.length) {
          openModal({
            title: 'Belum bisa diarsipkan',
            body: html`<p><b>${p.name}</b> masih dipakai atau direncanakan di: ${p.usedIn.map(function (u) { return u.machineCode + ' slot ' + u.slotNumber; }).join(', ')}.</p><p class="muted">Ganti produk di slot tersebut lewat menu Mesin &amp; slot, lalu arsipkan setelah operator menukarnya.</p>`,
            actions: [{ label: 'Mengerti', kind: 'btn-primary' }]
          });
          return;
        }
        confirmModal({
          title: 'Arsipkan produk?', confirmLabel: 'Arsipkan', danger: true,
          text: p.name + ' akan disembunyikan dari daftar. Riwayat transaksi tetap menampilkan namanya.',
          onConfirm: async function () { await api.archiveProduct(p.id); toast('Produk diarsipkan.'); rerender(); }
        });
      });
    });
    $$('[data-restore]', view).forEach(function (b) {
      b.addEventListener('click', async function () {
        try { await api.restoreProduct(Number(b.dataset.restore)); toast('Produk dipulihkan.'); rerender(); } catch (err) { handleError(err); }
      });
    });
  };

  function productForm(p, done) {
    var image = null;
    openModal({
      title: p ? 'Ubah produk' : 'Tambah produk',
      body: html`
        <div class="field"><label class="label" for="pf-name">Nama produk</label><input class="input" id="pf-name" name="name" maxlength="100" value="${p ? p.name : ''}" required><span class="field-error" data-error="name"></span></div>
        <div class="field"><label class="label" for="pf-desc">Deskripsi</label><textarea class="textarea" id="pf-desc" name="description" maxlength="300">${p ? p.description : ''}</textarea></div>
        <div class="field"><label class="label" for="pf-price">Harga (Rp)</label><input class="input" id="pf-price" name="price" type="number" inputmode="numeric" min="1" step="1" value="${p ? p.price : ''}" required>
          <span class="hint">Harga baru berlaku untuk pembelian berikutnya; transaksi lama menyimpan harganya sendiri.</span><span class="field-error" data-error="price"></span></div>
        <div class="field"><span class="label">Gambar</span>
          <div class="img-pick"><img id="pf-preview" src="${p && p.imageUrl ? p.imageUrl : ''}" alt="" ${p && p.imageUrl ? '' : raw('hidden')}><div class="field">
            <input id="pf-image" name="image" type="file" accept="image/png,image/jpeg">
            <span class="hint">JPG atau PNG, paling besar 2 MB. Rasio 3:4 tampil paling baik di kiosk.</span>
            <span class="field-error" data-error="image"></span></div></div></div>
        <p class="form-error" role="alert"></p>`,
      actions: [{ label: 'Batal' }, { label: p ? 'Simpan perubahan' : 'Tambah produk', kind: 'btn-primary', submit: true, onClick: async function (form) {
        var body = { name: form.elements.name.value, description: form.elements.description.value, price: Number(form.elements.price.value), imageUrl: image || (p ? p.imageUrl : null) };
        if (p) await api.updateProduct(p.id, body); else await api.createProduct(body);
        toast(p ? 'Produk disimpan.' : 'Produk ditambahkan.');
        done();
      } }],
      onMount: function (form) {
        form.elements.image.addEventListener('change', function () {
          var file = this.files[0];
          var err = $('[data-error="image"]', form);
          err.textContent = '';
          image = null;
          if (!file) return;
          if (!/^image\/(png|jpeg)$/.test(file.type)) { err.textContent = 'Gunakan file JPG atau PNG.'; this.value = ''; return; }
          if (file.size > 2 * 1024 * 1024) { err.textContent = 'Ukuran file ' + (file.size / 1048576).toFixed(1) + ' MB, maksimal 2 MB.'; this.value = ''; return; }
          var reader = new FileReader();
          reader.onload = function () { image = reader.result; var pv = $('#pf-preview'); pv.src = image; pv.hidden = false; };
          reader.readAsDataURL(file);
        });
      }
    });
  }

  /* ================================================================ 5. machines & slots */

  VIEWS.machines = async function (view, _id, alive) {
    await refreshMachines();
    if (!alive()) return;
    var ms = state.machines;
    view.innerHTML = str(html`
      <header class="page-head">
        <div><h1>Mesin &amp; slot</h1><p class="sub">${num(ms.length)} mesin · ${num(ms.filter(function (m) { return machineState(m).label === 'Online'; }).length)} online. Mesin baru cukup didaftarkan di sini lalu dipasang Kiosk Agent dengan kuncinya.</p></div>
        <div class="head-actions"><button class="btn btn-primary" type="button" id="mc-add">Tambah mesin</button></div>
      </header>
      <div class="machine-grid">${ms.map(function (m) {
        return html`<button class="card machine-card" type="button" data-machine="${m.id}">
          <div class="machine-top"><div><div class="machine-code">${m.code}</div><div class="machine-name">${m.name}</div><div class="machine-loc">${m.location || 'Lokasi belum diisi'}</div></div>${machinePill(m)}</div>
          <dl class="machine-stats">
            <div><dt>Pendapatan bulan ini</dt><dd>${rp(m.monthRevenue)}</dd></div>
            <div><dt>Slot</dt><dd>${m.slotCount}${m.emptySlots ? html` <span class="tag tag-bad">${m.emptySlots} kosong</span>` : ''}${m.blockedSlots ? html` <span class="tag tag-bad">${m.blockedSlots} perlu dicek</span>` : ''}</dd></div>
            <div><dt>Penjualan terakhir</dt><dd>${relTime(m.lastSaleAt)}</dd></div>
            <div><dt>Kontak terakhir</dt><dd>${relTime(m.lastSeenAt)}</dd></div>
          </dl></button>`;
      })}</div>`);
    $$('[data-machine]', view).forEach(function (b) { b.addEventListener('click', function () { go('machine-' + b.dataset.machine); }); });
    $('#mc-add', view).addEventListener('click', machineForm);
  };

  function machineForm() {
    openModal({
      title: 'Tambah mesin',
      body: html`
        <div class="form-grid">
          <div class="field"><label class="label" for="mf-code">Kode mesin</label><input class="input mono" id="mf-code" name="code" placeholder="VM-04" maxlength="20" required><span class="field-error" data-error="code"></span></div>
          <div class="field"><label class="label" for="mf-name">Nama</label><input class="input" id="mf-name" name="name" placeholder="Lobby Gedung B" maxlength="100" required><span class="field-error" data-error="name"></span></div>
          <div class="field span-2"><label class="label" for="mf-loc">Lokasi</label><input class="input" id="mf-loc" name="location" placeholder="Gedung B, lantai 1" maxlength="150"></div>
          <div class="field"><label class="label" for="mf-slots">Jumlah slot</label><input class="input" id="mf-slots" name="slotCount" type="number" min="1" max="60" value="6" required><span class="field-error" data-error="slotCount"></span></div>
          <div class="field"><label class="label" for="mf-cap">Kapasitas per slot</label><input class="input" id="mf-cap" name="capacity" type="number" min="1" max="30" value="10" required><span class="field-error" data-error="capacity"></span></div>
        </div>
        <p class="hint">Slot dibuat kosong. Produk yang Anda pilih per slot berlaku saat operator pertama kali mengisi mesin.</p>
        <p class="form-error" role="alert"></p>`,
      actions: [{ label: 'Batal' }, { label: 'Daftarkan mesin', kind: 'btn-primary', submit: true, onClick: async function (form) {
        var r = await api.createMachine({
          code: form.elements.code.value, name: form.elements.name.value, location: form.elements.location.value,
          slotCount: Number(form.elements.slotCount.value), capacity: Number(form.elements.capacity.value)
        });
        await refreshMachines();
        showMachineKey(r.machine, r.apiKey);
      } }]
    });
  }

  function showMachineKey(m, key, title) {
    openModal({
      title: title || 'Mesin ' + m.code + ' terdaftar',
      body: html`
        <p>Salin kunci mesin ini sekarang. Kunci hanya ditampilkan sekali; server hanya menyimpan hash-nya.</p>
        <div class="key-box"><code id="mk-key">${key}</code><button class="btn btn-sm" type="button" id="mk-copy">Salin</button></div>
        <p class="muted">Pasang Kiosk Agent di PC mesin, isi kunci ini di konfigurasinya, lalu jalankan kiosk. Mesin tampil Online setelah kontak pertama.</p>`,
      actions: [{ label: 'Buka mesin', kind: 'btn-primary', onClick: function () { go('machine-' + m.id); } }],
      onMount: function (form) {
        $('#mk-copy', form).addEventListener('click', function () {
          navigator.clipboard.writeText(key).then(function () { toast('Kunci disalin.'); }, function () {
            var range = document.createRange(); range.selectNodeContents($('#mk-key', form));
            var sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(range);
            toast('Teks kunci dipilih. Tekan Ctrl+C untuk menyalin.');
          });
        });
      }
    });
  }

  VIEWS.machine = async function (view, id, alive) {
    var results = await Promise.all([api.getMachine(id), api.getProducts({})]);
    if (!alive()) return;
    var m = results[0], products = results[1];
    var productOpts = function (selectedId, currentId) {
      return html`<option value="">Tidak ada perubahan</option>${products.filter(function (p) { return p.id !== currentId; }).map(function (p) {
        return html`<option value="${p.id}" ${String(selectedId) === String(p.id) ? raw('selected') : ''}>${p.name} · ${rp(p.price)}</option>`;
      })}`;
    };
    view.innerHTML = str(html`
      <header class="page-head">
        <div><a class="back-link" href="#machines" id="mc-back">← Semua mesin</a>
          <h1><span class="code">${m.code}</span> · ${m.name}</h1>
          <p class="sub">${m.location || 'Lokasi belum diisi'} · kontak terakhir ${relTime(m.lastSeenAt)} · versi katalog ${m.catalogVersion}</p></div>
        <div class="head-actions">${machinePill(m)}
          <button class="btn" type="button" id="mc-key">Buat kunci baru</button>
          <button class="btn ${m.isActive ? 'btn-danger' : ''}" type="button" id="mc-toggle">${m.isActive ? 'Nonaktifkan mesin' : 'Aktifkan lagi'}</button></div>
      </header>
      <section class="tiles">
        <div class="card tile"><span class="tile-label">Pendapatan bulan ini</span><span class="tile-value">${rp(m.monthRevenue)}</span><span class="tile-meta">${num(m.monthSales)} penjualan</span></div>
        <div class="card tile"><span class="tile-label">Slot</span><span class="tile-value">${m.slotCount}</span><span class="tile-meta">${m.emptySlots} kosong · ${m.blockedSlots} perlu dicek</span></div>
        <div class="card tile"><span class="tile-label">Penjualan terakhir</span><span class="tile-value" style="font-size:20px">${relTime(m.lastSaleAt)}</span><span class="tile-meta">${fmtDateTime(m.lastSaleAt)}</span></div>
      </section>
      <section class="card">
        <div class="card-head"><h2>Slot</h2><span class="hint">Produk berikutnya baru berlaku saat operator menukar isi slot dan mengonfirmasi restock, supaya layar kiosk dan isi coil berubah bersamaan.</span></div>
        <div class="table-wrap"><table class="table">
          <thead><tr><th class="num">Slot</th><th>Produk sekarang</th><th>Isi</th><th>Kapasitas</th><th>Produk berikutnya</th><th>Status</th><th></th></tr></thead>
          <tbody>${m.slots.map(function (s) {
            var pct = s.capacity ? s.qty / s.capacity * 100 : 0;
            return html`<tr data-slot="${s.slotNumber}">
              <td class="num"><b>${s.slotNumber}</b></td>
              <td>${s.product ? html`<b>${s.product.name}</b><div class="sub">${rp(s.product.price)}</div>` : html`<span class="muted">Belum ada produk</span>`}</td>
              <td><div class="fill"><div class="fill-track"><div class="fill-fill ${s.qty === 0 ? 'is-empty' : ''}" style="width:${pct.toFixed(0)}%"></div></div><span class="num">${s.qty}/${s.capacity}</span></div></td>
              <td><input class="input slot-input" type="number" min="1" max="30" name="capacity" id="cap-${s.slotNumber}" aria-label="Kapasitas slot ${s.slotNumber}" value="${s.capacity}"></td>
              <td><select class="select slot-select" name="nextProductId" id="next-${s.slotNumber}" aria-label="Produk berikutnya slot ${s.slotNumber}">${productOpts(s.nextProduct ? s.nextProduct.id : '', s.product ? s.product.id : null)}</select></td>
              <td>${s.needsCheck || s.qty === 0 || s.nextProduct ? html`<div class="actions" style="justify-content:flex-start">${s.needsCheck ? html`<span class="tag tag-bad">Perlu dicek</span>` : ''}${s.qty === 0 ? html`<span class="tag tag-bad">Kosong</span>` : ''}${s.nextProduct ? html`<span class="tag">Ganti saat restock</span>` : ''}</div>` : html`<span class="muted">—</span>`}</td>
              <td><button class="btn btn-sm" type="button" data-save="${s.slotNumber}" disabled>Simpan</button></td></tr>`;
          })}</tbody>
        </table></div>
      </section>`);

    $('#mc-back', view).addEventListener('click', function (e) { e.preventDefault(); go('machines'); });
    $$('tr[data-slot]', view).forEach(function (tr) {
      var n = Number(tr.dataset.slot);
      var s = m.slots.find(function (x) { return x.slotNumber === n; });
      var cap = $('input[name="capacity"]', tr), next = $('select[name="nextProductId"]', tr), save = $('[data-save]', tr);
      var dirty = function () {
        save.disabled = Number(cap.value) === s.capacity && String(next.value) === String(s.nextProduct ? s.nextProduct.id : '');
      };
      cap.addEventListener('input', dirty);
      next.addEventListener('change', dirty);
      save.addEventListener('click', async function () {
        save.disabled = true;
        cap.removeAttribute('aria-invalid');
        try {
          await api.updateSlot(m.id, n, { capacity: Number(cap.value), nextProductId: next.value ? Number(next.value) : null });
          toast('Slot ' + n + ' disimpan.');
          route('machine-' + m.id);
        } catch (err) {
          if (err.code === 'VALIDATION') {
            if (err.data.fields.capacity) cap.setAttribute('aria-invalid', 'true');
            toast(Object.keys(err.data.fields).map(function (k) { return err.data.fields[k]; }).join(' '), true);
          } else handleError(err);
          dirty();
        }
      });
    });
    $('#mc-key', view).addEventListener('click', function () {
      confirmModal({
        title: 'Buat kunci baru untuk ' + m.code + '?', confirmLabel: 'Buat kunci baru', danger: true,
        text: 'Kunci lama langsung tidak berlaku, jadi kiosk berhenti menjual sampai kunci baru dipasang di Kiosk Agent mesin ini.',
        onConfirm: async function () {
          var r = await api.rotateMachineKey(m.id);
          setTimeout(function () { showMachineKey(r.machine, r.apiKey, 'Kunci baru ' + m.code); }, 50);
        }
      });
    });
    $('#mc-toggle', view).addEventListener('click', function () {
      if (!m.isActive) {
        api.setMachineActive(m.id, true).then(function () { toast(m.code + ' aktif lagi.'); refreshMachines().then(function () { route('machine-' + m.id); }); }, handleError);
        return;
      }
      confirmModal({
        title: 'Nonaktifkan ' + m.code + '?', confirmLabel: 'Nonaktifkan', danger: true,
        text: 'Kunci mesin ini akan ditolak, jadi kiosk tidak bisa menjual sampai diaktifkan lagi. Riwayat penjualan tetap tersimpan.',
        onConfirm: async function () { await api.setMachineActive(m.id, false); toast(m.code + ' dinonaktifkan.'); await refreshMachines(); route('machine-' + m.id); }
      });
    });
  };

  /* ================================================================ 6. operators */

  VIEWS.operators = async function (view, _id, alive) {
    var list = await api.getOperators();
    if (!alive()) return;
    view.innerHTML = str(html`
      <header class="page-head">
        <div><h1>Operator</h1><p class="sub">Akun untuk login restock di kiosk. Admin juga bisa melakukan restock.</p></div>
        <div class="head-actions"><button class="btn btn-primary" type="button" id="op-add">Tambah operator</button></div>
      </header>
      <section class="card"><div class="table-wrap"><table class="table">
        <thead><tr><th>Nama</th><th>Username</th><th>Peran</th><th class="num">Kunjungan 30 hari</th><th>Kunjungan terakhir</th><th>Status</th><th></th></tr></thead>
        <tbody>${list.map(function (u) {
          return html`<tr>
            <td><b>${u.name}</b></td>
            <td><span class="mono">${u.username}</span></td>
            <td>${u.role === 'ADMIN' ? 'Admin' : 'Operator'}</td>
            <td class="num">${num(u.visits30)}</td>
            <td class="nowrap">${u.lastVisitAt ? html`${relTime(u.lastVisitAt)} <span class="sub">${u.lastVisitMachine}</span>` : html`<span class="muted">—</span>`}</td>
            <td>${u.isActive ? html`<span class="pill pill-good">Aktif</span>` : html`<span class="pill">Nonaktif</span>`}</td>
            <td><div class="actions">${u.role === 'ADMIN' ? html`<span class="muted">—</span>` : html`
              <button class="btn btn-sm" type="button" data-reset="${u.id}" ${u.isActive ? '' : raw('disabled')}>Atur ulang password</button>
              <button class="btn btn-sm ${u.isActive ? 'btn-danger' : ''}" type="button" data-toggle="${u.id}">${u.isActive ? 'Nonaktifkan' : 'Aktifkan'}</button>`}</div></td></tr>`;
        })}</tbody>
      </table></div></section>`);

    var rerender = function () { route('operators'); };
    $('#op-add', view).addEventListener('click', function () {
      openModal({
        title: 'Tambah operator',
        body: html`
          <div class="field"><label class="label" for="of-name">Nama</label><input class="input" id="of-name" name="name" maxlength="100" required><span class="field-error" data-error="name"></span></div>
          <div class="field"><label class="label" for="of-user">Username</label><input class="input mono" id="of-user" name="username" maxlength="32" autocomplete="off" required><span class="hint">Huruf kecil, angka, titik atau garis bawah. Dipakai untuk login di kiosk.</span><span class="field-error" data-error="username"></span></div>
          <div class="field"><label class="label" for="of-pass">Password awal</label><input class="input" id="of-pass" name="password" type="password" minlength="6" autocomplete="new-password" required><span class="field-error" data-error="password"></span></div>
          <p class="form-error" role="alert"></p>`,
        actions: [{ label: 'Batal' }, { label: 'Tambah operator', kind: 'btn-primary', submit: true, onClick: async function (form) {
          await api.createOperator({ name: form.elements.name.value, username: form.elements.username.value, password: form.elements.password.value });
          toast('Operator ditambahkan.');
          rerender();
        } }]
      });
    });
    $$('[data-reset]', view).forEach(function (b) {
      b.addEventListener('click', function () {
        var u = list.find(function (x) { return String(x.id) === b.dataset.reset; });
        confirmModal({
          title: 'Atur ulang password ' + u.name + '?', confirmLabel: 'Atur ulang',
          text: 'Password lama langsung tidak berlaku. Anda akan mendapat password sementara untuk diberikan ke operator.',
          onConfirm: async function () {
            var r = await api.resetOperatorPassword(u.id);
            setTimeout(function () {
              openModal({
                title: 'Password sementara',
                body: html`<p>Berikan password ini ke <b>${u.name}</b>. Password hanya ditampilkan sekali.</p><div class="key-box"><code>${r.temporaryPassword}</code></div>`,
                actions: [{ label: 'Selesai', kind: 'btn-primary' }]
              });
            }, 50);
          }
        });
      });
    });
    $$('[data-toggle]', view).forEach(function (b) {
      b.addEventListener('click', function () {
        var u = list.find(function (x) { return String(x.id) === b.dataset.toggle; });
        if (!u.isActive) {
          api.setOperatorActive(u.id, true).then(function () { toast(u.name + ' aktif lagi.'); rerender(); }, handleError);
          return;
        }
        confirmModal({
          title: 'Nonaktifkan ' + u.name + '?', confirmLabel: 'Nonaktifkan', danger: true,
          text: 'Operator ini tidak bisa login di kiosk lagi. Riwayat restock-nya tetap tersimpan.',
          onConfirm: async function () { await api.setOperatorActive(u.id, false); toast(u.name + ' dinonaktifkan.'); rerender(); }
        });
      });
    });
  };

  /* ================================================================ 7. change log */

  var ENTITY = { PRODUCT: 'Produk', MACHINE: 'Mesin', SLOT: 'Slot', USER: 'Akun' };
  var ACTION = { CREATE: 'Membuat', UPDATE: 'Mengubah', ARCHIVE: 'Mengarsipkan', DEACTIVATE: 'Menonaktifkan' };
  var FIELD = { price: 'harga', name: 'nama', description: 'deskripsi', image: 'gambar', isActive: 'aktif', capacity: 'kapasitas', nextProduct: 'produk berikutnya', code: 'kode', slots: 'jumlah slot', role: 'peran', username: 'username', password: 'password', apiKey: 'kunci mesin', location: 'lokasi' };

  function fmtVal(k, v) {
    if (v === null || v === undefined || v === '') return '—';
    if (k === 'price') return rp(v);
    if (k === 'isActive') return v ? 'ya' : 'tidak';
    return String(v);
  }

  VIEWS.changelog = async function (view, _id, alive) {
    var f = state.changelog;
    var r = f.preset === '90d' ? presetRange('90d') : presetRange('30d');
    var list = await api.getAuditLogs({ from: r.from, to: r.to, entityType: f.entityType });
    if (!alive()) return;
    view.innerHTML = str(html`
      <header class="page-head"><div><h1>Log perubahan</h1><p class="sub">Siapa mengubah harga, produk, slot, mesin atau akun, kapan, dan dari apa ke apa. Log tidak bisa diubah atau dihapus.</p></div></header>
      <div class="filters">
        <div class="seg" role="group" aria-label="Periode">
          <button type="button" data-cl-preset="30d" aria-pressed="${f.preset === '30d'}">30 hari</button>
          <button type="button" data-cl-preset="90d" aria-pressed="${f.preset === '90d'}">90 hari</button>
        </div>
        <div class="field"><label class="label" for="cl-type">Jenis</label><select class="select" id="cl-type"><option value="">Semua jenis</option>${Object.keys(ENTITY).map(function (k) {
          return html`<option value="${k}" ${f.entityType === k ? raw('selected') : ''}>${ENTITY[k]}</option>`;
        })}</select></div>
      </div>
      <section class="card"><div class="table-wrap"><table class="table">
        <thead><tr><th>Waktu</th><th>Oleh</th><th>Aksi</th><th>Objek</th><th>Perubahan</th></tr></thead>
        <tbody>${list.length ? list.map(function (a) {
          var keys = Object.keys(a.after || {});
          return html`<tr>
            <td class="nowrap">${fmtDateTime(a.createdAt)}</td>
            <td>${a.user.name}</td>
            <td>${ACTION[a.action] || a.action}</td>
            <td><span class="sub">${ENTITY[a.entityType] || a.entityType}</span><div><b>${a.entityLabel}</b></div></td>
            <td><ul class="changes">${keys.map(function (k) {
              return html`<li><span class="k">${FIELD[k] || k}:</span> ${a.before ? html`<del>${fmtVal(k, a.before[k])}</del> → ` : ''}<ins>${fmtVal(k, a.after[k])}</ins></li>`;
            })}</ul></td></tr>`;
        }) : html`<tr class="empty-row"><td colspan="5">Belum ada perubahan di periode ini.</td></tr>`}</tbody>
      </table></div></section>`);
    $$('[data-cl-preset]', view).forEach(function (b) { b.addEventListener('click', function () { f.preset = b.dataset.clPreset; route('changelog'); }); });
    $('#cl-type', view).addEventListener('change', function () { f.entityType = this.value; route('changelog'); });
  };

  /* ================================================================ boot */

  window.Admin = { state: state, go: go };

  (async function boot() {
    if (api.hasToken()) {
      try { var me = await api.me(); await enterApp(me); return; } catch (_) { /* token expired */ }
    }
    showLogin();
  })();
})();
