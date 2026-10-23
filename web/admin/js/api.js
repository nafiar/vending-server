/*
 * AdminApi: every call the admin web makes to the server.
 *
 * Real mode: JSON over HTTPS to `${apiBase}/admin/...` with a Bearer token.
 * Mock mode: the same functions run on an in-memory store seeded with three machines
 * and about 90 days of sales, so the admin web can be developed and demoed alone.
 *
 * Errors are thrown as ApiError with a `code`, for example:
 *   OFFLINE, UNAUTHORIZED, INVALID_LOGIN, VALIDATION (data.fields), CONFLICT (data.reason), NOT_FOUND
 */
(function (global) {
  'use strict';

  var cfg = Object.assign({ mock: true, apiBase: '/api', timeoutMs: 15000 }, global.ADMIN_CONFIG || {});

  function ApiError(code, message, data) {
    var e = new Error(message || code);
    e.name = 'ApiError';
    e.code = code;
    e.data = data || {};
    return e;
  }

  /* ================================================================ real */

  var token = null;
  try { token = global.sessionStorage.getItem('admin_token'); } catch (_) { /* storage blocked */ }

  function setToken(t) {
    token = t;
    try {
      if (t) global.sessionStorage.setItem('admin_token', t);
      else global.sessionStorage.removeItem('admin_token');
    } catch (_) { /* storage blocked */ }
  }

  function qs(params) {
    var parts = [];
    Object.keys(params || {}).forEach(function (k) {
      var v = params[k];
      if (v !== undefined && v !== null && v !== '') parts.push(encodeURIComponent(k) + '=' + encodeURIComponent(v));
    });
    return parts.length ? '?' + parts.join('&') : '';
  }

  async function http(method, path, body) {
    var ctrl = new AbortController();
    var timer = setTimeout(function () { ctrl.abort(); }, cfg.timeoutMs);
    var res;
    try {
      var headers = { 'Content-Type': 'application/json' };
      if (token) headers.Authorization = 'Bearer ' + token;
      res = await fetch(cfg.apiBase + path, {
        method: method, headers: headers, signal: ctrl.signal, cache: 'no-store',
        body: body === undefined ? undefined : JSON.stringify(body)
      });
    } catch (err) {
      throw ApiError('OFFLINE', 'Server tidak terjangkau. Periksa koneksi lalu coba lagi.');
    } finally {
      clearTimeout(timer);
    }
    var data = null;
    try { data = await res.json(); } catch (_) { /* empty body */ }
    if (res.status === 401) setToken(null);
    if (!res.ok) throw ApiError((data && data.code) || 'HTTP_' + res.status, data && data.message, data);
    return data;
  }

  var realApi = {
    login: async function (username, password) {
      var r = await http('POST', '/admin/login', { username: username, password: password });
      setToken(r.token);
      return r;
    },
    logout: async function () { try { await http('POST', '/admin/logout', {}); } finally { setToken(null); } },
    me: function () { return http('GET', '/admin/me'); },
    getSummary: function (p) { return http('GET', '/admin/recap/summary' + qs(p)); },
    getRecap: function (p) { return http('GET', '/admin/recap' + qs(p)); },
    getTransactions: function (p) { return http('GET', '/admin/transactions' + qs(p)); },
    getRestockSessions: function (p) { return http('GET', '/admin/restock-sessions' + qs(p)); },
    getProducts: function (p) { return http('GET', '/admin/products' + qs(p)); },
    createProduct: function (b) { return http('POST', '/admin/products', b); },
    updateProduct: function (id, b) { return http('PUT', '/admin/products/' + id, b); },
    archiveProduct: function (id) { return http('POST', '/admin/products/' + id + '/archive', {}); },
    restoreProduct: function (id) { return http('POST', '/admin/products/' + id + '/restore', {}); },
    getMachines: function () { return http('GET', '/admin/machines'); },
    getMachine: function (id) { return http('GET', '/admin/machines/' + id); },
    createMachine: function (b) { return http('POST', '/admin/machines', b); },
    setMachineActive: function (id, active) { return http('POST', '/admin/machines/' + id + (active ? '/activate' : '/deactivate'), {}); },
    rotateMachineKey: function (id) { return http('POST', '/admin/machines/' + id + '/rotate-key', {}); },
    updateSlot: function (machineId, slotNumber, b) { return http('PUT', '/admin/machines/' + machineId + '/slots/' + slotNumber, b); },
    getOperators: function () { return http('GET', '/admin/operators'); },
    createOperator: function (b) { return http('POST', '/admin/operators', b); },
    setOperatorActive: function (id, active) { return http('POST', '/admin/operators/' + id + (active ? '/activate' : '/deactivate'), {}); },
    resetOperatorPassword: function (id) { return http('POST', '/admin/operators/' + id + '/reset-password', {}); },
    getAuditLogs: function (p) { return http('GET', '/admin/audit-logs' + qs(p)); },
    resetDemo: function () { return http('POST', '/admin/demo/reset', {}); }
  };

  /* ================================================================ mock data */

  var DAY = 86400000;

  function mulberry(seed) {
    return function () {
      seed = (seed + 0x6D2B79F5) >>> 0;
      var t = seed;
      t = Math.imul(t ^ (t >>> 15), t | 1);
      t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }
  var rnd = mulberry(20261013);

  function fakeUuid() {
    var h = '';
    for (var i = 0; i < 32; i++) h += Math.floor(rnd() * 16).toString(16);
    return h.slice(0, 8) + '-' + h.slice(8, 12) + '-4' + h.slice(13, 16) + '-a' + h.slice(17, 20) + '-' + h.slice(20, 32);
  }

  function startOfDay(t) { var d = new Date(t); d.setHours(0, 0, 0, 0); return d.getTime(); }
  function dayKey(t) {
    var d = new Date(t);
    return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
  }
  function parseDay(s) { var p = s.split('-').map(Number); return new Date(p[0], p[1] - 1, p[2]).getTime(); }

  var PALETTE = ['#7b3fa0', '#1f7a8c', '#c0392b', '#2e8b57', '#d35400', '#34495e', '#8e44ad', '#b07d12', '#5d6d7e'];
  function placeholderImage(name, i) {
    var color = PALETTE[i % PALETTE.length];
    var initials = name.split(/\s+/).map(function (w) { return w[0]; }).join('').slice(0, 2).toUpperCase();
    var svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 300 400"><rect width="300" height="400" rx="18" fill="' + color + '"/>' +
      '<rect x="18" y="18" width="264" height="364" rx="12" fill="none" stroke="#fff" stroke-opacity=".35" stroke-width="4"/>' +
      '<circle cx="150" cy="170" r="74" fill="#fff" fill-opacity=".16"/>' +
      '<text x="150" y="196" font-family="Arial, sans-serif" font-size="76" font-weight="700" fill="#fff" text-anchor="middle">' + initials + '</text>' +
      '<text x="150" y="320" font-family="Arial, sans-serif" font-size="30" font-weight="600" fill="#fff" fill-opacity=".9" text-anchor="middle">GACHA</text></svg>';
    return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg);
  }

  var now = Date.now();
  var today0 = startOfDay(now);

  var store = {
    users: [
      { id: 1, name: 'Admin Demo', username: 'admin', password: 'admin123', role: 'ADMIN', isActive: true, createdAt: today0 - 120 * DAY },
      { id: 2, name: 'Budi Santoso', username: 'budi', password: 'budi123', role: 'OPERATOR', isActive: true, createdAt: today0 - 100 * DAY },
      { id: 3, name: 'Siti Rahma', username: 'siti', password: 'siti123', role: 'OPERATOR', isActive: true, createdAt: today0 - 70 * DAY },
      { id: 4, name: 'Andi Pratama', username: 'andi', password: 'andi123', role: 'OPERATOR', isActive: false, createdAt: today0 - 90 * DAY }
    ],
    products: [],
    machines: [],
    slots: [],
    transactions: [],
    restockSessions: [],
    auditLogs: [],
    seq: { product: 0, machine: 0, user: 4, session: 0, audit: 0 }
  };

  function addProduct(name, description, price, createdDaysAgo, active) {
    var id = ++store.seq.product;
    var t = today0 - createdDaysAgo * DAY + 9 * 3600000;
    store.products.push({
      id: id, name: name, description: description, price: price, imageUrl: placeholderImage(name, id),
      isActive: active !== false, createdAt: t, updatedAt: t
    });
    return id;
  }

  addProduct('Gacha Legendary', 'Satu pack berisi 5 kartu, peluang kartu legendaris.', 50000, 110);
  addProduct('Gacha Ultra Rare', 'Satu pack berisi 5 kartu, minimal 1 ultra rare.', 35000, 110);
  addProduct('Gacha Rare', 'Satu pack berisi 5 kartu, minimal 1 rare.', 25000, 110);
  addProduct('Gacha Uncommon', 'Satu pack berisi 5 kartu uncommon.', 15000, 110);
  addProduct('Gacha Common', 'Satu pack berisi 5 kartu common.', 10000, 110);
  addProduct('Gacha Mystery Box', 'Isi acak dari semua tingkat.', 30000, 110);
  addProduct('Gacha Promo Edition', 'Edisi promo terbatas.', 40000, 6);
  addProduct('Gacha Starter Pack', 'Paket pemula, 3 kartu dan 1 sleeve.', 20000, 62);
  addProduct('Gacha Holiday 2025', 'Edisi liburan akhir tahun.', 45000, 100, false);

  var PRICE_CHANGE_AT = today0 - 20 * DAY + 10 * 3600000; // product 2: 30.000 -> 35.000
  function priceAt(productId, t) {
    if (productId === 2 && t < PRICE_CHANGE_AT) return 30000;
    return store.products[productId - 1].price;
  }

  function addMachine(code, name, location, layout, createdDaysAgo, lastSeenMinutesAgo, sales) {
    var id = ++store.seq.machine;
    store.machines.push({
      id: id, code: code, name: name, location: location, isActive: true,
      catalogVersion: 1 + Math.floor(rnd() * 40), apiKeyHash: 'argon2id$…',
      createdAt: today0 - createdDaysAgo * DAY + 8 * 3600000,
      lastSeenAt: now - lastSeenMinutesAgo * 60000, dailySales: sales
    });
    layout.forEach(function (productId, i) {
      store.slots.push({
        machineId: id, slotNumber: i + 1, productId: productId, nextProductId: null,
        capacity: 10, qty: Math.floor(rnd() * 9) + 1, needsCheck: false
      });
    });
    return id;
  }

  addMachine('VM-01', 'Lobby Gedung A', 'Gedung A, lantai 1', [1, 2, 3, 4, 5, 6], 95, 2, 12);
  addMachine('VM-02', 'Kantin Utama', 'Kantin, lantai dasar', [1, 2, 3, 4, 5, 6, 8, 5], 60, 1, 18);
  addMachine('VM-03', 'Perpustakaan', 'Perpustakaan, lantai 2', [8, 5, 4, 3, 6, 2], 30, 190, 7);

  function slotOf(machineId, slotNumber) {
    return store.slots.find(function (s) { return s.machineId === machineId && s.slotNumber === slotNumber; });
  }
  slotOf(1, 3).nextProductId = 7;
  slotOf(1, 4).qty = 0;
  slotOf(2, 6).needsCheck = true;

  // ---- sales history
  var POPULARITY = { 1: 0.7, 2: 0.9, 3: 1.2, 4: 1.4, 5: 1.8, 6: 1.0, 7: 0.8, 8: 1.3, 9: 0.6 };
  var HOURS = [7, 8, 9, 9, 10, 11, 12, 12, 12, 13, 13, 14, 15, 15, 16, 17, 17, 18, 19];

  store.machines.forEach(function (m) {
    var mSlots = store.slots.filter(function (s) { return s.machineId === m.id; });
    var weights = mSlots.map(function (s) { return POPULARITY[s.productId] || 1; });
    var totalW = weights.reduce(function (a, b) { return a + b; }, 0);
    for (var day = startOfDay(m.createdAt); day <= today0; day += DAY) {
      var dow = new Date(day).getDay();
      var factor = dow === 0 || dow === 6 ? 0.45 : 1;
      var n = Math.max(0, Math.round(m.dailySales * factor * (0.7 + rnd() * 0.6)));
      for (var i = 0; i < n; i++) {
        var t = day + HOURS[Math.floor(rnd() * HOURS.length)] * 3600000 + Math.floor(rnd() * 3600000);
        if (t > now - 5 * 60000 || t < m.createdAt) continue;
        var pick = rnd() * totalW, k = 0;
        while (pick > weights[k]) { pick -= weights[k]; k++; }
        var slot = mSlots[Math.min(k, mSlots.length - 1)];
        var productId = slot.productId === 7 ? 3 : slot.productId; // promo is newer than most history
        var r = rnd();
        var status = r < 0.915 ? 'DISPENSED' : r < 0.955 ? 'CANCELLED' : r < 0.985 ? 'EXPIRED' : 'FAILED';
        var tx = {
          id: fakeUuid(), machineId: m.id, slotNumber: slot.slotNumber, productId: productId,
          price: priceAt(productId, t), status: status, failureReason: null, customerReportedAt: null,
          createdAt: t, paidAt: null, finishedAt: t + 15000 + Math.floor(rnd() * 30000)
        };
        if (status === 'DISPENSED' || status === 'FAILED') tx.paidAt = t + 12000;
        if (status === 'FAILED') tx.failureReason = rnd() < 0.7 ? 'MOTOR_TIMEOUT' : 'COIL_NOT_HOME';
        if (status === 'EXPIRED') tx.finishedAt = t + 60000;
        if (status === 'DISPENSED' && rnd() < 0.012) tx.customerReportedAt = tx.finishedAt + 20000;
        store.transactions.push(tx);
      }
    }
  });
  store.transactions.sort(function (a, b) { return a.createdAt - b.createdAt; });

  // ---- restock visits: about two a day per machine
  store.machines.forEach(function (m) {
    var mSlots = store.slots.filter(function (s) { return s.machineId === m.id; });
    for (var day = startOfDay(m.createdAt) + DAY; day <= today0; day += DAY) {
      var visits = new Date(day).getDay() === 0 ? [10] : [9, 14];
      visits.forEach(function (h) {
        var start = day + h * 3600000 + Math.floor(rnd() * 40) * 60000;
        if (start > now - 30 * 60000) return;
        var operatorId = start < today0 - 15 * DAY && rnd() < 0.25 ? 4 : rnd() < 0.55 ? 2 : 3;
        var items = mSlots.map(function (s) {
          var before = Math.floor(rnd() * 7);
          var after = s.productId === 1 ? Math.max(before, 3 + Math.floor(rnd() * 3)) : s.capacity;
          if (rnd() < 0.15) after = before;
          return { slotNumber: s.slotNumber, productId: s.productId, qtyBefore: before, qtyAfter: after, productChanged: false };
        });
        store.restockSessions.push({
          id: ++store.seq.session, machineId: m.id, userId: operatorId,
          startedAt: start, finishedAt: start + (4 + Math.floor(rnd() * 9)) * 60000, note: null, items: items
        });
      });
    }
  });
  store.restockSessions.sort(function (a, b) { return a.startedAt - b.startedAt; });

  // ---- change log
  function audit(userId, action, entityType, entityId, label, before, after, at) {
    store.auditLogs.push({
      id: ++store.seq.audit, userId: userId, action: action, entityType: entityType, entityId: entityId,
      entityLabel: label, before: before || null, after: after || null, createdAt: at || Date.now()
    });
  }
  audit(1, 'CREATE', 'MACHINE', 2, 'VM-02 Kantin Utama', null, { code: 'VM-02', name: 'Kantin Utama', slots: 8 }, store.machines[1].createdAt);
  audit(1, 'CREATE', 'PRODUCT', 8, 'Gacha Starter Pack', null, { name: 'Gacha Starter Pack', price: 20000 }, store.products[7].createdAt);
  audit(1, 'CREATE', 'MACHINE', 3, 'VM-03 Perpustakaan', null, { code: 'VM-03', name: 'Perpustakaan', slots: 6 }, store.machines[2].createdAt);
  audit(1, 'UPDATE', 'PRODUCT', 2, 'Gacha Ultra Rare', { price: 30000 }, { price: 35000 }, PRICE_CHANGE_AT);
  audit(1, 'DEACTIVATE', 'USER', 4, 'Andi Pratama (andi)', { isActive: true }, { isActive: false }, today0 - 15 * DAY + 11 * 3600000);
  audit(1, 'ARCHIVE', 'PRODUCT', 9, 'Gacha Holiday 2025', { isActive: true }, { isActive: false }, today0 - 10 * DAY + 15 * 3600000);
  audit(1, 'CREATE', 'PRODUCT', 7, 'Gacha Promo Edition', null, { name: 'Gacha Promo Edition', price: 40000 }, store.products[6].createdAt);
  audit(1, 'UPDATE', 'SLOT', 13, 'VM-01 · Slot 3', { nextProduct: null }, { nextProduct: 'Gacha Promo Edition' }, today0 - 2 * DAY + 16 * 3600000);
  store.auditLogs.sort(function (a, b) { return a.createdAt - b.createdAt; });

  /* ================================================================ mock helpers */

  var session = null; // { token, userId }

  function sleep(ms) { return new Promise(function (r) { setTimeout(r, ms); }); }
  function clone(v) { return JSON.parse(JSON.stringify(v)); }
  async function mock(fn, ms) {
    await sleep(ms === undefined ? 160 : ms);
    return clone(await fn());
  }
  function requireAdmin() {
    if (!session) throw ApiError('UNAUTHORIZED', 'Sesi berakhir. Silakan masuk lagi.');
    return store.users.find(function (u) { return u.id === session.userId; });
  }
  function productById(id) { return store.products.find(function (p) { return p.id === id; }); }
  function machineById(id) { return store.machines.find(function (m) { return m.id === id; }); }
  function userById(id) { return store.users.find(function (u) { return u.id === id; }); }

  function rangeOf(p) {
    var from = p.from ? parseDay(p.from) : today0 - 29 * DAY;
    var to = (p.to ? parseDay(p.to) : today0) + DAY - 1;
    return { from: from, to: to };
  }

  function txIn(p) {
    var r = rangeOf(p);
    var mid = p.machineId ? Number(p.machineId) : null;
    return store.transactions.filter(function (t) {
      return t.createdAt >= r.from && t.createdAt <= r.to && (!mid || t.machineId === mid);
    });
  }

  function sum(rows) {
    var out = { revenue: 0, sales: 0 };
    rows.forEach(function (t) { if (t.status === 'DISPENSED') { out.revenue += t.price; out.sales += 1; } });
    return out;
  }

  function isoWeekStart(t) {
    var d = new Date(t);
    var dow = (d.getDay() + 6) % 7; // Monday = 0
    return startOfDay(t) - dow * DAY;
  }

  function periodKey(t, groupBy) {
    if (groupBy === 'month') { var d = new Date(t); return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0'); }
    if (groupBy === 'week') return dayKey(isoWeekStart(t));
    return dayKey(t);
  }

  function periodsBetween(from, to, groupBy) {
    var keys = [];
    var t = groupBy === 'week' ? isoWeekStart(from) : groupBy === 'month' ? new Date(new Date(from).getFullYear(), new Date(from).getMonth(), 1).getTime() : startOfDay(from);
    while (t <= to) {
      keys.push({ key: periodKey(t, groupBy), start: t });
      if (groupBy === 'month') { var d = new Date(t); t = new Date(d.getFullYear(), d.getMonth() + 1, 1).getTime(); }
      else t += (groupBy === 'week' ? 7 : 1) * DAY;
    }
    return keys;
  }

  function txRow(t) {
    var m = machineById(t.machineId), p = productById(t.productId);
    return {
      id: t.id, createdAt: t.createdAt, paidAt: t.paidAt, finishedAt: t.finishedAt,
      machine: { id: m.id, code: m.code, name: m.name }, slotNumber: t.slotNumber,
      product: { id: p.id, name: p.name }, price: t.price, status: t.status,
      failureReason: t.failureReason, customerReportedAt: t.customerReportedAt
    };
  }

  function slotView(s) {
    var p = s.productId ? productById(s.productId) : null;
    var n = s.nextProductId ? productById(s.nextProductId) : null;
    return {
      slotNumber: s.slotNumber, capacity: s.capacity, qty: s.qty, needsCheck: s.needsCheck,
      product: p ? { id: p.id, name: p.name, price: p.price, imageUrl: p.imageUrl } : null,
      nextProduct: n ? { id: n.id, name: n.name } : null
    };
  }

  function machineView(m) {
    var monthStart = new Date(new Date(now).getFullYear(), new Date(now).getMonth(), 1).getTime();
    var mtx = store.transactions.filter(function (t) { return t.machineId === m.id; });
    var month = sum(mtx.filter(function (t) { return t.createdAt >= monthStart; }));
    var last = null;
    for (var i = mtx.length - 1; i >= 0; i--) if (mtx[i].status === 'DISPENSED') { last = mtx[i].createdAt; break; }
    var slots = store.slots.filter(function (s) { return s.machineId === m.id; });
    return {
      id: m.id, code: m.code, name: m.name, location: m.location, isActive: m.isActive,
      catalogVersion: m.catalogVersion, createdAt: m.createdAt, lastSeenAt: m.lastSeenAt, lastSaleAt: last,
      slotCount: slots.length, emptySlots: slots.filter(function (s) { return s.qty === 0 || !s.productId; }).length,
      blockedSlots: slots.filter(function (s) { return s.needsCheck; }).length,
      monthRevenue: month.revenue, monthSales: month.sales
    };
  }

  function bumpMachinesSelling(productId) {
    store.machines.forEach(function (m) {
      var sells = store.slots.some(function (s) { return s.machineId === m.id && s.productId === productId; });
      if (sells) m.catalogVersion += 1;
    });
  }

  function validateProduct(b) {
    var f = {};
    var name = (b.name || '').trim();
    if (!name) f.name = 'Nama wajib diisi.';
    else if (name.length > 100) f.name = 'Nama paling panjang 100 karakter.';
    var price = Number(b.price);
    if (!Number.isInteger(price) || price <= 0) f.price = 'Harga harus bilangan bulat lebih dari 0.';
    if (b.imageUrl && !/^data:image\/(png|jpeg);/.test(b.imageUrl) && !/^https?:/.test(b.imageUrl) && !/^data:image\/svg/.test(b.imageUrl)) f.image = 'Gambar harus JPG atau PNG.';
    if (Object.keys(f).length) throw ApiError('VALIDATION', 'Periksa isian yang ditandai.', { fields: f });
    return { name: name, description: (b.description || '').trim(), price: price, imageUrl: b.imageUrl || null };
  }

  function diff(before, after) {
    var b = {}, a = {};
    Object.keys(after).forEach(function (k) { if (JSON.stringify(before[k]) !== JSON.stringify(after[k])) { b[k] = before[k]; a[k] = after[k]; } });
    return { before: b, after: a, changed: Object.keys(a).length > 0 };
  }

  function tempPassword() {
    var chars = 'abcdefghjkmnpqrstuvwxyz23456789';
    var s = '';
    for (var i = 0; i < 8; i++) s += chars[Math.floor(Math.random() * chars.length)];
    return s;
  }

  /* ================================================================ mock api */

  var mockApi = {
    login: function (username, password) {
      return mock(function () {
        var u = store.users.find(function (x) { return x.username === (username || '').trim().toLowerCase(); });
        if (!u || u.password !== password) throw ApiError('INVALID_LOGIN', 'Username atau password salah.');
        if (u.role !== 'ADMIN') throw ApiError('FORBIDDEN', 'Akun operator tidak bisa masuk ke panel admin.');
        if (!u.isActive) throw ApiError('FORBIDDEN', 'Akun ini dinonaktifkan.');
        session = { token: fakeUuid(), userId: u.id };
        setToken(session.token);
        return { token: session.token, name: u.name, username: u.username };
      }, 350);
    },

    logout: function () { return mock(function () { session = null; setToken(null); return { ok: true }; }, 60); },

    me: function () {
      return mock(function () {
        if (!session && token) session = { token: token, userId: 1 }; // keep the demo signed in across reloads
        var u = requireAdmin();
        return { name: u.name, username: u.username };
      }, 60);
    },

    getSummary: function (p) {
      return mock(function () {
        requireAdmin();
        var mid = p && p.machineId ? Number(p.machineId) : null;
        var monthStart = new Date(new Date(now).getFullYear(), new Date(now).getMonth(), 1).getTime();
        var base = store.transactions.filter(function (t) { return !mid || t.machineId === mid; });
        var since = function (t0) { return sum(base.filter(function (t) { return t.createdAt >= t0; })); };
        var yesterday = sum(base.filter(function (t) { return t.createdAt >= today0 - DAY && t.createdAt < today0 - DAY + (now - today0); }));
        return { today: since(today0), sameTimeYesterday: yesterday, last7: since(today0 - 6 * DAY), month: since(monthStart) };
      });
    },

    getRecap: function (p) {
      return mock(function () {
        requireAdmin();
        var groupBy = p.groupBy || 'day';
        var r = rangeOf(p);
        var rows = txIn(p);
        var totals = sum(rows);
        totals.failed = rows.filter(function (t) { return t.status === 'FAILED'; }).length;
        totals.reported = rows.filter(function (t) { return t.customerReportedAt; }).length;
        totals.cancelled = rows.filter(function (t) { return t.status === 'CANCELLED' || t.status === 'EXPIRED'; }).length;

        var byPeriod = {};
        rows.forEach(function (t) {
          if (t.status !== 'DISPENSED') return;
          var k = periodKey(t.createdAt, groupBy);
          byPeriod[k] = byPeriod[k] || { revenue: 0, sales: 0 };
          byPeriod[k].revenue += t.price;
          byPeriod[k].sales += 1;
        });
        var series = periodsBetween(r.from, r.to, groupBy).map(function (k) {
          var v = byPeriod[k.key] || { revenue: 0, sales: 0 };
          return { period: k.key, start: k.start, revenue: v.revenue, sales: v.sales };
        });

        var machines = store.machines.filter(function (m) { return !p.machineId || m.id === Number(p.machineId); });
        var byMachine = machines.map(function (m) {
          var mr = rows.filter(function (t) { return t.machineId === m.id; });
          var s = sum(mr);
          var last = null;
          for (var i = mr.length - 1; i >= 0; i--) if (mr[i].status === 'DISPENSED') { last = mr[i].createdAt; break; }
          return { machineId: m.id, code: m.code, name: m.name, location: m.location, isActive: m.isActive, revenue: s.revenue, sales: s.sales, lastSaleAt: last, lastSeenAt: m.lastSeenAt };
        }).sort(function (a, b) { return b.revenue - a.revenue; });

        var group = function (keyFn, labelFn) {
          var acc = {};
          rows.forEach(function (t) {
            if (t.status !== 'DISPENSED') return;
            var k = keyFn(t);
            acc[k] = acc[k] || { key: k, label: labelFn(t), revenue: 0, sales: 0 };
            acc[k].revenue += t.price;
            acc[k].sales += 1;
          });
          return Object.keys(acc).map(function (k) { return acc[k]; }).sort(function (a, b) { return b.revenue - a.revenue; });
        };
        var byProduct = group(function (t) { return t.productId; }, function (t) { return productById(t.productId).name; });
        var breakdown = p.breakdown === 'slot'
          ? group(function (t) { return t.machineId + '-' + t.slotNumber; }, function (t) {
              var m = machineById(t.machineId), s = slotOf(t.machineId, t.slotNumber), prod = s && s.productId ? productById(s.productId) : null;
              return m.code + ' · Slot ' + t.slotNumber + (prod ? ' (' + prod.name + ')' : '');
            })
          : byProduct;

        return { from: dayKey(r.from), to: dayKey(r.to), groupBy: groupBy, totals: totals, series: series, byMachine: byMachine, topProducts: byProduct.slice(0, 5), breakdown: breakdown };
      }, 220);
    },

    getTransactions: function (p) {
      return mock(function () {
        requireAdmin();
        var rows = txIn(p).filter(function (t) {
          if (p.slotNumber && t.slotNumber !== Number(p.slotNumber)) return false;
          if (p.status && t.status !== p.status) return false;
          if (p.flag === 'reported' && !t.customerReportedAt) return false;
          if (p.flag === 'problem' && !(t.customerReportedAt || t.status === 'FAILED')) return false;
          return true;
        }).slice().reverse();
        var pageSize = Number(p.pageSize) || 25, page = Math.max(1, Number(p.page) || 1);
        var s = sum(rows);
        return {
          total: rows.length, page: page, pageSize: pageSize, dispensedRevenue: s.revenue, dispensedCount: s.sales,
          rows: (p.all ? rows : rows.slice((page - 1) * pageSize, page * pageSize)).map(txRow)
        };
      }, 200);
    },

    getRestockSessions: function (p) {
      return mock(function () {
        requireAdmin();
        var r = rangeOf(p);
        var rows = store.restockSessions.filter(function (s) {
          return s.startedAt >= r.from && s.startedAt <= r.to &&
            (!p.machineId || s.machineId === Number(p.machineId)) && (!p.userId || s.userId === Number(p.userId));
        }).slice().reverse();
        var pageSize = Number(p.pageSize) || 20, page = Math.max(1, Number(p.page) || 1);
        var view = function (s) {
          var m = machineById(s.machineId), u = userById(s.userId);
          return {
            id: s.id, startedAt: s.startedAt, finishedAt: s.finishedAt, note: s.note,
            machine: { id: m.id, code: m.code, name: m.name }, operator: { id: u.id, name: u.name, username: u.username },
            itemsAdded: s.items.reduce(function (a, it) { return a + Math.max(0, it.qtyAfter - it.qtyBefore); }, 0),
            slotsChanged: s.items.filter(function (it) { return it.qtyAfter !== it.qtyBefore || it.productChanged; }).length,
            items: s.items.map(function (it) { return Object.assign({}, it, { productName: productById(it.productId).name }); })
          };
        };
        return { total: rows.length, page: page, pageSize: pageSize, rows: (p.all ? rows : rows.slice((page - 1) * pageSize, page * pageSize)).map(view) };
      }, 200);
    },

    getProducts: function (p) {
      return mock(function () {
        requireAdmin();
        return store.products.filter(function (x) { return (p && p.includeArchived) || x.isActive; }).map(function (x) {
          var usedIn = store.slots.filter(function (s) { return s.productId === x.id || s.nextProductId === x.id; }).map(function (s) {
            return { machineCode: machineById(s.machineId).code, slotNumber: s.slotNumber, planned: s.productId !== x.id };
          });
          return Object.assign({}, x, { usedIn: usedIn });
        });
      });
    },

    createProduct: function (b) {
      return mock(function () {
        var u = requireAdmin();
        var v = validateProduct(b);
        var id = ++store.seq.product;
        var t = Date.now();
        var p = { id: id, name: v.name, description: v.description, price: v.price, imageUrl: v.imageUrl || placeholderImage(v.name, id), isActive: true, createdAt: t, updatedAt: t };
        store.products.push(p);
        audit(u.id, 'CREATE', 'PRODUCT', id, p.name, null, { name: p.name, price: p.price });
        return p;
      }, 300);
    },

    updateProduct: function (id, b) {
      return mock(function () {
        var u = requireAdmin();
        var p = productById(Number(id));
        if (!p) throw ApiError('NOT_FOUND', 'Produk tidak ditemukan.');
        var v = validateProduct(b);
        var d = diff({ name: p.name, description: p.description, price: p.price, image: p.imageUrl }, { name: v.name, description: v.description, price: v.price, image: v.imageUrl || p.imageUrl });
        if (!d.changed) return p;
        p.name = v.name; p.description = v.description; p.price = v.price; if (v.imageUrl) p.imageUrl = v.imageUrl;
        p.updatedAt = Date.now();
        if (d.before.image) { d.before.image = '(gambar lama)'; d.after.image = '(gambar baru)'; }
        audit(u.id, 'UPDATE', 'PRODUCT', p.id, p.name, d.before, d.after);
        bumpMachinesSelling(p.id);
        return p;
      }, 300);
    },

    archiveProduct: function (id) {
      return mock(function () {
        var u = requireAdmin();
        var p = productById(Number(id));
        var used = store.slots.filter(function (s) { return s.productId === p.id || s.nextProductId === p.id; });
        if (used.length) {
          throw ApiError('CONFLICT', 'Produk masih dipakai di slot.', {
            reason: 'IN_USE', slots: used.map(function (s) { return machineById(s.machineId).code + ' slot ' + s.slotNumber; })
          });
        }
        p.isActive = false; p.updatedAt = Date.now();
        audit(u.id, 'ARCHIVE', 'PRODUCT', p.id, p.name, { isActive: true }, { isActive: false });
        return p;
      }, 250);
    },

    restoreProduct: function (id) {
      return mock(function () {
        var u = requireAdmin();
        var p = productById(Number(id));
        p.isActive = true; p.updatedAt = Date.now();
        audit(u.id, 'UPDATE', 'PRODUCT', p.id, p.name, { isActive: false }, { isActive: true });
        return p;
      }, 250);
    },

    getMachines: function () { return mock(function () { requireAdmin(); return store.machines.map(machineView); }); },

    getMachine: function (id) {
      return mock(function () {
        requireAdmin();
        var m = machineById(Number(id));
        if (!m) throw ApiError('NOT_FOUND', 'Mesin tidak ditemukan.');
        var v = machineView(m);
        v.slots = store.slots.filter(function (s) { return s.machineId === m.id; }).map(slotView);
        return v;
      });
    },

    createMachine: function (b) {
      return mock(function () {
        var u = requireAdmin();
        var f = {};
        var code = (b.code || '').trim().toUpperCase();
        if (!/^[A-Z0-9-]{2,20}$/.test(code)) f.code = 'Kode 2–20 karakter: huruf, angka atau tanda hubung.';
        else if (store.machines.some(function (m) { return m.code === code; })) f.code = 'Kode ' + code + ' sudah dipakai.';
        if (!(b.name || '').trim()) f.name = 'Nama wajib diisi.';
        var n = Number(b.slotCount), cap = Number(b.capacity);
        if (!Number.isInteger(n) || n < 1 || n > 60) f.slotCount = 'Jumlah slot 1 sampai 60.';
        if (!Number.isInteger(cap) || cap < 1 || cap > 30) f.capacity = 'Kapasitas 1 sampai 30.';
        if (Object.keys(f).length) throw ApiError('VALIDATION', 'Periksa isian yang ditandai.', { fields: f });
        var id = ++store.seq.machine;
        var m = { id: id, code: code, name: b.name.trim(), location: (b.location || '').trim(), isActive: true, catalogVersion: 1, apiKeyHash: 'argon2id$…', createdAt: Date.now(), lastSeenAt: null };
        store.machines.push(m);
        for (var i = 1; i <= n; i++) store.slots.push({ machineId: id, slotNumber: i, productId: null, nextProductId: null, capacity: cap, qty: 0, needsCheck: false });
        var key = 'vmk_' + fakeUuid().replace(/-/g, '') + fakeUuid().replace(/-/g, '').slice(0, 8);
        audit(u.id, 'CREATE', 'MACHINE', id, code + ' ' + m.name, null, { code: code, name: m.name, slots: n, capacity: cap });
        return { machine: machineView(m), apiKey: key };
      }, 400);
    },

    setMachineActive: function (id, active) {
      return mock(function () {
        var u = requireAdmin();
        var m = machineById(Number(id));
        m.isActive = !!active;
        audit(u.id, active ? 'UPDATE' : 'DEACTIVATE', 'MACHINE', m.id, m.code + ' ' + m.name, { isActive: !active }, { isActive: !!active });
        return machineView(m);
      }, 250);
    },

    rotateMachineKey: function (id) {
      return mock(function () {
        var u = requireAdmin();
        var m = machineById(Number(id));
        if (!m) throw ApiError('NOT_FOUND', 'Mesin tidak ditemukan.');
        var key = 'vmk_' + fakeUuid().replace(/-/g, '') + fakeUuid().replace(/-/g, '').slice(0, 11);
        audit(u.id, 'UPDATE', 'MACHINE', m.id, m.code + ' ' + m.name, { apiKey: '(kunci lama)' }, { apiKey: '(kunci baru)' });
        return { machine: machineView(m), apiKey: key };
      }, 300);
    },

    updateSlot: function (machineId, slotNumber, b) {
      return mock(function () {
        var u = requireAdmin();
        var m = machineById(Number(machineId));
        var s = slotOf(m.id, Number(slotNumber));
        var f = {};
        var cap = Number(b.capacity);
        if (!Number.isInteger(cap) || cap < 1 || cap > 30) f.capacity = 'Kapasitas 1 sampai 30.';
        else if (cap < s.qty) f.capacity = 'Tidak boleh di bawah isi saat ini (' + s.qty + ').';
        var next = b.nextProductId ? Number(b.nextProductId) : null;
        if (next && (!productById(next) || !productById(next).isActive)) f.nextProductId = 'Pilih produk yang aktif.';
        if (Object.keys(f).length) throw ApiError('VALIDATION', 'Periksa isian yang ditandai.', { fields: f });
        if (next === s.productId) next = null;
        var name = function (pid) { return pid ? productById(pid).name : null; };
        var d = diff({ capacity: s.capacity, nextProduct: name(s.nextProductId) }, { capacity: cap, nextProduct: name(next) });
        s.capacity = cap; s.nextProductId = next;
        if (d.changed) audit(u.id, 'UPDATE', 'SLOT', m.id * 100 + s.slotNumber, m.code + ' · Slot ' + s.slotNumber, d.before, d.after);
        return slotView(s);
      }, 250);
    },

    getOperators: function () {
      return mock(function () {
        requireAdmin();
        return store.users.map(function (u) {
          var last = null;
          for (var i = store.restockSessions.length - 1; i >= 0; i--) {
            if (store.restockSessions[i].userId === u.id) { last = store.restockSessions[i]; break; }
          }
          var count30 = store.restockSessions.filter(function (s) { return s.userId === u.id && s.startedAt >= today0 - 29 * DAY; }).length;
          return {
            id: u.id, name: u.name, username: u.username, role: u.role, isActive: u.isActive, createdAt: u.createdAt,
            lastVisitAt: last ? last.startedAt : null, lastVisitMachine: last ? machineById(last.machineId).code : null, visits30: count30
          };
        });
      });
    },

    createOperator: function (b) {
      return mock(function () {
        var admin = requireAdmin();
        var f = {};
        var username = (b.username || '').trim().toLowerCase();
        if (!(b.name || '').trim()) f.name = 'Nama wajib diisi.';
        if (!/^[a-z0-9._]{3,32}$/.test(username)) f.username = '3–32 karakter: huruf kecil, angka, titik atau garis bawah.';
        else if (store.users.some(function (u) { return u.username === username; })) f.username = 'Username sudah dipakai.';
        if ((b.password || '').length < 6) f.password = 'Paling sedikit 6 karakter.';
        if (Object.keys(f).length) throw ApiError('VALIDATION', 'Periksa isian yang ditandai.', { fields: f });
        var u = { id: ++store.seq.user, name: b.name.trim(), username: username, password: b.password, role: 'OPERATOR', isActive: true, createdAt: Date.now() };
        store.users.push(u);
        audit(admin.id, 'CREATE', 'USER', u.id, u.name + ' (' + u.username + ')', null, { name: u.name, username: u.username, role: 'OPERATOR' });
        return { id: u.id };
      }, 300);
    },

    setOperatorActive: function (id, active) {
      return mock(function () {
        var admin = requireAdmin();
        var u = userById(Number(id));
        if (u.role === 'ADMIN') throw ApiError('CONFLICT', 'Akun admin tidak bisa dinonaktifkan dari sini.');
        u.isActive = !!active;
        audit(admin.id, active ? 'UPDATE' : 'DEACTIVATE', 'USER', u.id, u.name + ' (' + u.username + ')', { isActive: !active }, { isActive: !!active });
        return { ok: true };
      }, 250);
    },

    resetOperatorPassword: function (id) {
      return mock(function () {
        var admin = requireAdmin();
        var u = userById(Number(id));
        var pw = tempPassword();
        u.password = pw;
        audit(admin.id, 'UPDATE', 'USER', u.id, u.name + ' (' + u.username + ')', { password: '••••' }, { password: '(diatur ulang)' });
        return { temporaryPassword: pw };
      }, 300);
    },

    getAuditLogs: function (p) {
      return mock(function () {
        requireAdmin();
        var r = rangeOf(Object.assign({ from: dayKey(today0 - 119 * DAY) }, p));
        return store.auditLogs.filter(function (a) {
          return a.createdAt >= r.from && a.createdAt <= r.to && (!p.entityType || a.entityType === p.entityType);
        }).slice().reverse().map(function (a) {
          var u = userById(a.userId);
          return Object.assign({}, a, { user: { id: u.id, name: u.name } });
        });
      });
    }
  };

  global.AdminApi = Object.assign({}, cfg.mock ? mockApi : realApi, {
    isMock: cfg.mock,
    hasToken: function () { return !!token; },
    ApiError: ApiError
  });
})(window);
