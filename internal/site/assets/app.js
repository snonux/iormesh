// iormesh explorer (internal/site; docs/visualization.md "The local
// site"). One hand-written file, no dependencies, inlined into
// index.html and allowed there by its sha256 in the page's CSP.
//
// Every string of the model came from the traced servers and is
// hostile: it reaches the page only through textContent and DOM
// attributes this file chooses (createElement / createElementNS). No
// HTML parsing of data, no string-built markup, no eval, no URL from data
// except "#/…" routes whose parts are encodeURIComponent'd. The page
// makes no request: the model is read from the inert JSON block the
// build embedded, and the CSP (default-src 'none') would refuse any
// request anyway.
'use strict';
(function () {
  const SVGNS = 'http://www.w3.org/2000/svg';
  const M = readModel();
  const filter = { host: '', text: '', from: '', to: '' };
  const tabs = [['mesh', 'Mesh'], ['convs', 'Conversations'], ['threads', 'Threads'],
    ['procs', 'Processes'], ['health', 'Health']];

  // ---- data access -------------------------------------------------

  // readModel parses the embedded model; a missing or broken block
  // yields an empty model, so the page still renders and says so.
  function readModel() {
    const node = document.getElementById('model');
    try {
      return JSON.parse(node ? node.textContent : '{}') || {};
    } catch (e) {
      return { broken: String(e) };
    }
  }

  function arr(x) { return Array.isArray(x) ? x : []; }

  // clean makes invisible or reordering characters visible, as the
  // text views' termsafe does: a control or bidi character in a trace
  // string must not hide or reorder what the page shows.
  function clean(s) {
    return String(s === undefined || s === null ? '' : s).replace(
      /[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u2028\u2029\u202a-\u202e\u2066-\u2069\ufeff]/g,
      function (c) { return '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'); });
  }

  function short(s, n) {
    s = clean(s);
    return s.length > n ? s.slice(0, n - 1) + '…' : s;
  }

  function dur(ns) {
    if (!ns) { return '-'; }
    if (ns >= 1e9) { return (ns / 1e9).toFixed(2) + 's'; }
    if (ns >= 1e6) { return (ns / 1e6).toFixed(2) + 'ms'; }
    return (ns / 1e3).toFixed(1) + 'µs';
  }

  // ---- DOM helpers -------------------------------------------------

  // el makes an element with a fixed class and text content; kids are
  // nodes or strings (strings become text nodes, never markup).
  function el(tag, cls, kids) {
    const n = document.createElement(tag);
    if (cls) { n.className = cls; }
    add(n, kids);
    return n;
  }

  function add(n, kids) {
    if (kids === undefined || kids === null) { return n; }
    if (!Array.isArray(kids)) { kids = [kids]; }
    for (const k of kids) {
      if (k === undefined || k === null) { continue; }
      n.appendChild(typeof k === 'object' ? k : document.createTextNode(clean(k)));
    }
    return n;
  }

  // link is an in-page route: its parts are encoded, so a hostile ID
  // cannot leave the "#/" namespace.
  function link(parts, text) {
    const a = el('a', '', text);
    a.href = route(parts);
    return a;
  }

  function route(parts) { return '#/' + parts.map(function (p) { return encodeURIComponent(String(p)); }).join('/'); }

  function svg(tag, attrs, cls) {
    const n = document.createElementNS(SVGNS, tag);
    for (const k in attrs) { n.setAttribute(k, String(attrs[k])); }
    if (cls) { n.setAttribute('class', cls); }
    return n;
  }

  function stext(x, y, s, anchor, cls) {
    const t = svg('text', { x: x, y: y, 'text-anchor': anchor || 'start' }, cls);
    t.textContent = clean(s);
    return t;
  }

  function table(head, rows) {
    if (rows.length === 0) { return el('p', 'empty', 'none'); }
    const t = el('table');
    t.appendChild(el('tr', '', head.map(function (h) { return el('th', '', h); })));
    for (const r of rows) {
      const tr = el('tr', r.sel ? 'sel' : '', r.cells.map(function (c) { return el('td', '', c); }));
      t.appendChild(tr);
    }
    return t;
  }

  function more(n, what) { return n > 0 ? el('p', 'more', 'truncated: ' + n + ' more ' + what) : null; }

  // partial says the processes list is partial when the rollup hit its
  // tracking cap (the model's untracked, task 2i4), as `show processes`
  // does (viewmodel.PartialLine); nothing for a complete rollup.
  function partial(n) {
    return n > 0 ? el('p', 'warn', 'partial: ' + n + ' events past the rollup’s tracking cap are not in it') : null;
  }

  function kv(pairs) {
    return table(['', ''], pairs.filter(function (p) { return p[1] !== undefined && p[1] !== ''; })
      .map(function (p) { return { cells: [p[0], p[1]] }; }));
  }

  // ---- filter ------------------------------------------------------

  // hit says whether any of strs contains the filter text (case-blind).
  function hit(strs) {
    if (!filter.text) { return true; }
    const q = filter.text.toLowerCase();
    return strs.some(function (s) { return String(s || '').toLowerCase().indexOf(q) >= 0; });
  }

  // inTime compares a model instant with the from/to filter: a full
  // RFC 3339 value compares as a string (all UTC), a bare HH:MM[:SS]
  // against the instant's time of day.
  function inTime(t) {
    if (!t) { return !filter.from && !filter.to; }
    const pick = function (f) { return f.length <= 12 ? t.slice(11, 11 + f.length) : t.slice(0, f.length); };
    if (filter.from && pick(filter.from) < filter.from) { return false; }
    if (filter.to && pick(filter.to) >= filter.to) { return false; }
    return true;
  }

  function hostsOfService(name) {
    const hs = arr(M.processes).filter(function (p) {
      return p.service === name || arr(p.serves).indexOf(name) >= 0;
    }).map(function (p) { return p.host; });
    return hs;
  }

  // serviceShown filters a mesh service. Under a host filter it is
  // shown when a process of it runs in that host directory, or one of
  // its endpoints is on that host (endpointOn).
  function serviceShown(s) {
    if (filter.host && hostsOfService(s.name).indexOf(filter.host) < 0 &&
      !arr(s.endpoints).some(endpointOn)) { return false; }
    return hit([s.name].concat(arr(s.endpoints), arr(s.top_paths).map(function (p) { return p.method + ' ' + p.path; })));
  }

  // endpointOn says whether endpoint label e — host:port, ip:port or
  // [ip6]:port, plus @<host id> for a loopback or link-local address
  // scoped to the traced host it was seen on (servicedb
  // Endpoint.Label) — is on the filter's host. A scoped endpoint is
  // that host's by its scope alone: the host id (/etc/machine-id, a
  // pod's own id), matched against the host id of the directory's
  // sessions. Any other is by its whole address equal to the directory
  // or one of its sessions' FQDNs, never by a prefix (h1 is not h10,
  // and the directory is no FQDN: task mj4). Two directories sharing
  // an FQDN both own an unscoped endpoint naming it: the label cannot
  // tell them apart.
  function endpointOn(e) {
    e = String(e || '');
    const at = e.lastIndexOf('@');
    if (at >= 0) {
      const id = e.slice(at + 1);
      return hostSessions().some(function (s) { return !!s.host_id && s.host_id === id; });
    }
    const colon = e.lastIndexOf(':');
    return colon > 0 && hostMatch(e.slice(0, colon).replace(/^\[(.*)\]$/, '$1'));
  }

  // hostMatch says whether name — a step's, lifeline's or thread's
  // host (a session's FQDN), or an endpoint's — is the filter's host.
  // The dropdown lists the store's host directories (run.hosts), which
  // are not the FQDNs, so the sessions link the two (task 4i4); the
  // directory itself still matches, for a session that named no FQDN.
  function hostMatch(name) {
    if (name === filter.host) { return true; }
    return hostSessions().some(function (s) { return !!s.fqdn && s.fqdn === name; });
  }

  // hostSessions are the sessions recorded in the filter's host
  // directory.
  function hostSessions() {
    return arr((M.health || {}).sessions).filter(function (s) { return s.host === filter.host; });
  }

  // refOnHost says whether ref — a raw event's
  // <project>/<host dir>/<fqdn>-<session>/<seq> (federate sessionKey) —
  // was recorded in the filter's host directory: the exact directory,
  // so two directories sharing one FQDN keep apart (task mj4).
  function refOnHost(ref) {
    ref = String(ref || '');
    return hostSessions().some(function (s) { return ref.indexOf(s.project + '/' + filter.host + '/') === 0; });
  }

  // onHost says whether a step or thread — its refs, and its hosts,
  // which are sessions' FQDNs — is the filter host's: by its refs when
  // it has any, else by a host that hostMatch links to the directory
  // (an FQDN two directories share then matches both).
  function onHost(refs, hosts) {
    const rs = refs.filter(Boolean);
    return rs.length ? rs.some(refOnHost) : hosts.some(hostMatch);
  }

  function convShown(c) {
    const steps = arr(c.steps);
    if (filter.host && !steps.some(function (s) { return onHost([s.ref], [s.host, hostOfLane(c, s.lane)]); })) {
      return false;
    }
    if (!inTime(c.start)) { return false; }
    return hit([c.id, c.entry, c.label].concat(arr(c.services), steps.map(stepWords)));
  }

  function hostOfLane(c, i) { const l = arr(c.lifelines)[i]; return l ? l.host : ''; }

  function stepWords(s) {
    return [s.label, s.service, s.to, s.thread, s.host, s.ref].concat(arr(s.stack)).join(' ');
  }

  // ownActs are the thread story acts whose entry carries the thread's
  // own ref; a receive or response entry carries the step's, which is
  // the peer's (viewmodel storyBook.mark), so it never places the
  // thread on a host.
  const ownActs = ['send', 'reply', 'file'];

  function storyShown(t) {
    const own = arr(t.entries).filter(function (e) { return ownActs.indexOf(e.act) >= 0; });
    if (filter.host && !onHost(own.map(function (e) { return e.ref; }), [t.host])) { return false; }
    return hit([t.service, t.thread, t.host, String(t.pid), String(t.tid)].concat(
      arr(t.entries).map(function (e) { return e.label + ' ' + e.peer; })));
  }

  function procShown(p) {
    if (filter.host && p.host !== filter.host) { return false; }
    return hit([p.host, p.fqdn, String(p.pid), p.comm, p.app, p.service, p.container, p.container_id]
      .concat(arr(p.serves),
      arr(p.threads).map(function (t) { return t.name; }), arr(p.files).map(function (f) { return f.path; })));
  }

  // buildFilters makes the filter bar. The host dropdown lists the
  // store's host directories (run.hosts); refOnHost matches one to the
  // conversations' and threads' refs, hostMatch to its sessions' FQDNs
  // for what has no ref and for the mesh's endpoints.
  function buildFilters() {
    const box = document.getElementById('filters');
    const host = el('select');
    host.appendChild(option('', 'all hosts'));
    for (const h of arr(M.run && M.run.hosts)) { host.appendChild(option(h, h)); }
    const text = input('service, thread, pid, path, method, frame', 34);
    const from = input('from (HH:MM:SS or RFC 3339)', 24);
    const to = input('to', 12);
    const bind = function (n, key) {
      n.addEventListener('input', function () { filter[key] = n.value.trim(); render(); });
    };
    bind(host, 'host'); bind(text, 'text'); bind(from, 'from'); bind(to, 'to');
    host.addEventListener('change', function () { filter.host = host.value; render(); });
    add(box, [el('label', '', 'filter'), host, text, from, to]);
  }

  function option(v, label) { const o = el('option', '', label); o.value = v; return o; }

  function input(ph, size) { const i = el('input'); i.placeholder = ph; i.size = size; return i; }

  // ---- pan and zoom ------------------------------------------------

  // panZoom makes svg pannable (drag) and zoomable (wheel, buttons);
  // g carries the transform. Clicks after a drag are swallowed, so a
  // pan never selects the node it ended on.
  function panZoom(root, g, fit) {
    const st = { x: fit.x, y: fit.y, k: fit.k, drag: null, moved: false };
    const apply = function () { g.setAttribute('transform', 'translate(' + st.x + ' ' + st.y + ') scale(' + st.k + ')'); };
    root.addEventListener('wheel', function (ev) {
      ev.preventDefault();
      zoomAt(st, ev.deltaY < 0 ? 1.15 : 1 / 1.15, ev.offsetX, ev.offsetY); apply();
    }, { passive: false });
    root.addEventListener('pointerdown', function (ev) {
      st.drag = { x: ev.clientX, y: ev.clientY, ox: st.x, oy: st.y }; st.moved = false;
    });
    root.addEventListener('pointermove', function (ev) {
      if (!st.drag) { return; }
      const dx = ev.clientX - st.drag.x, dy = ev.clientY - st.drag.y;
      if (Math.abs(dx) + Math.abs(dy) > 4) { st.moved = true; root.classList.add('drag'); }
      st.x = st.drag.ox + dx; st.y = st.drag.oy + dy; apply();
    });
    const end = function () { st.drag = null; root.classList.remove('drag'); };
    root.addEventListener('pointerup', end);
    root.addEventListener('pointerleave', end);
    root.addEventListener('click', function (ev) { if (st.moved) { ev.stopPropagation(); ev.preventDefault(); } }, true);
    apply();
    return {
      zoom: function (f) { zoomAt(st, f, root.clientWidth / 2, root.clientHeight / 2); apply(); },
      reset: function () { st.x = fit.x; st.y = fit.y; st.k = fit.k; apply(); }
    };
  }

  function zoomAt(st, f, cx, cy) {
    const k = Math.min(8, Math.max(0.1, st.k * f));
    st.x = cx - (cx - st.x) * (k / st.k); st.y = cy - (cy - st.y) * (k / st.k); st.k = k;
  }

  // canvas is an SVG viewport of the given content size with zoom
  // buttons; returns the group to draw into.
  function canvas(parent, w, h) {
    const root = svg('svg', {}, 'canvas');
    const defs = svg('defs', {});
    const mk = svg('marker', { id: 'arrow', viewBox: '0 0 10 10', refX: 10, refY: 5, markerWidth: 7,
      markerHeight: 7, orient: 'auto-start-reverse' });
    mk.appendChild(svg('path', { d: 'M0,0 L10,5 L0,10 z' }));
    defs.appendChild(mk); root.appendChild(defs);
    const g = svg('g', {}); root.appendChild(g);
    const bar = el('div', 'toolbar');
    parent.appendChild(bar); parent.appendChild(root);
    const vw = root.clientWidth || 900, vh = root.clientHeight || 600;
    const k = Math.min(1, vw / Math.max(w, 1), vh / Math.max(h, 1));
    const pz = panZoom(root, g, { x: 10, y: 10, k: Math.max(k, 0.1) });
    add(bar, [button('+', function () { pz.zoom(1.25); }), button('−', function () { pz.zoom(0.8); }),
      button('fit', pz.reset), el('span', 'muted', 'drag to pan, wheel to zoom')]);
    return g;
  }

  function button(label, fn) { const b = el('button', '', label); b.type = 'button'; b.addEventListener('click', fn); return b; }

  // ---- mesh --------------------------------------------------------

  // meshLayout ranks services left to right by the longest client →
  // server path (bounded by the node count, so a cycle cannot loop),
  // then stacks each rank in model order: a pure function of the
  // sorted model, as the SVG exports' layout is.
  function meshLayout() {
    const names = arr(M.services).map(function (s) { return s.name; });
    for (const e of arr(M.edges)) {
      for (const n of [e.from, e.to]) { if (names.indexOf(n) < 0) { names.push(n); } }
    }
    // Prototype-free maps: a service may be named "__proto__" or
    // "constructor", and a plain object would hand back its prototype.
    const rank = Object.create(null);
    names.forEach(function (n) { rank[n] = 0; });
    for (let i = 0; i < names.length; i++) {
      let changed = false;
      for (const e of arr(M.edges)) {
        if (e.from !== e.to && rank[e.to] < rank[e.from] + 1 && rank[e.from] + 1 < names.length) {
          rank[e.to] = rank[e.from] + 1; changed = true;
        }
      }
      if (!changed) { break; }
    }
    const rows = Object.create(null), pos = Object.create(null);
    names.forEach(function (n) { const r = rank[n]; rows[r] = (rows[r] || 0) + 1; pos[n] = { x: r * 280, y: (rows[r] - 1) * 60 }; });
    return { names: names, pos: pos };
  }

  // renderMesh draws the services and edges, and below them the routed
  // edges (routedBand). Without routes or flags the screen is what it
  // was before routed edges existed: no band, no extra legend.
  function renderMesh(view, sel) {
    const lay = meshLayout(), rs = arr(M.routes);
    let w = 0, h = 0;
    for (const n of lay.names) { w = Math.max(w, lay.pos[n].x + 200); h = Math.max(h, lay.pos[n].y + 40); }
    const top = h + (h > 0 ? 40 : 0);
    if (rs.length > 0) {
      for (const r of rs) { w = Math.max(w, routedHops(r).length * HOPSTEP + 400); }
      h = top + 30 + rs.length * 44;
    }
    add(view, el('h2', '', 'Mesh — ' + lay.names.length + ' services, ' + arr(M.edges).length + ' edges' +
      (rs.length ? ', ' + rs.length + ' routed' : '')));
    add(view, el('p', 'muted', 'solid: fused · dashed: unresolved · click a service or an edge' +
      (rs.length ? ' · purple dashed: routed, completed from the route map (not traced)' : '')));
    flagNote(view);
    const g = canvas(view, w, h);
    arr(M.edges).forEach(function (e, i) { g.appendChild(meshEdge(e, i, lay.pos, sel)); });
    lay.names.forEach(function (n) { g.appendChild(meshNode(n, lay.pos[n], sel)); });
    if (rs.length > 0) { routedBand(g, top, sel); }
  }

  // ---- routed edges (route map, PLAN.md M7) ------------------------

  // A routed edge is one row: client → FQDN → ingress → Service →
  // workload (→ pod), the hops no tracer saw drawn as boxes of their
  // own kind. Only a fused edge (both ends traced) is drawn solid; one
  // the route map completed is dashed, so it never reads as traced.
  const HOPSTEP = 230;
  const HOPKINDS = ['client', 'fqdn', 'ingress', 'service', 'workload', 'pod'];

  // routedHops are r's hops with their kinds, the empty ones left out.
  function routedHops(r) {
    const vals = [r.from, r.fqdn, r.ingress, r.service, r.workload, r.pod], out = [];
    vals.forEach(function (v, i) { if (i === 0 || v) { out.push({ kind: HOPKINDS[i], name: v || '' }); } });
    return out;
  }

  // routeNote and flagText are the Go twins viewmodel.RouteNote and
  // viewmodel.FlagText (what `build` prints): keep their wording in step.
  function routeNote(r) {
    let s = r.basis + ', ' + (r.conns || 0) + ' connections';
    if (r.basis === 'service' && !r.pod && r.endpoints > 1) { s += '; ' + r.endpoints + ' endpoints, pod unknown'; }
    return s;
  }

  function flagText(f) {
    return f.kind + ' ' + f.subject + ': ' + f.detail + (f.conns > 0 ? ' (' + f.conns + ' connections)' : '');
  }

  // flagsOf are the route flags about r: subject one of its hops past
  // the client (viewmodel.FlagsOf).
  function flagsOf(r) {
    const hops = routedHops(r).slice(1).map(function (h) { return h.name; });
    return arr((M.health || {}).route_flags).filter(function (f) { return f.subject && hops.indexOf(f.subject) >= 0; });
  }

  function routeShown(r) { return hit(routedHops(r).map(function (h) { return h.name; })); }

  // flagNote puts a warning above the mesh when the route map and the
  // traces disagree somewhere: a flag is never left to the health
  // screen alone.
  function flagNote(view) {
    const fs = arr((M.health || {}).route_flags);
    if (fs.length === 0) { return; }
    add(view, el('p', 'warn', [fs.length + ' route flag' + (fs.length > 1 ? 's' : '') +
      ' — where the route map and the traces disagree: ', link(['mesh', 'flag', 0], 'see them')]));
  }

  function routedBand(g, top, sel) {
    g.appendChild(stext(0, top + 14, 'routed edges (route map)', 'start', 'label'));
    arr(M.routes).forEach(function (r, i) { g.appendChild(routedRow(r, i, top + 30 + i * 44, sel)); });
  }

  function routedRow(r, i, y, sel) {
    const basis = ['fused', 'service', 'route'].indexOf(r.basis) >= 0 ? r.basis : 'other';
    let cls = 'routed ' + basis;
    if (!routeShown(r)) { cls += ' dim'; }
    if (sel && sel.kind === 'route' && sel.i === i) { cls += ' sel'; }
    const g = svg('g', {}, cls), hops = routedHops(r);
    hops.forEach(function (h, k) {
      const x = k * HOPSTEP;
      if (k > 0) {
        g.appendChild(svg('line', { x1: x - (HOPSTEP - 200), y1: y + 18, x2: x, y2: y + 18, 'marker-end': 'url(#arrow)' }, 'hopline'));
      }
      const b = svg('g', {}, 'hop ' + h.kind);
      b.appendChild(svg('rect', { x: x, y: y, width: 200, height: 36, rx: 4 }));
      b.appendChild(stext(x + 8, y + 15, short(h.name, 28)));
      b.appendChild(stext(x + 8, y + 29, h.kind, 'start', 'label'));
      g.appendChild(b);
    });
    g.appendChild(stext(hops.length * HOPSTEP - 22, y + 22, short(routeNote(r), 60), 'start', 'label'));
    const t = svg('title', {}); t.textContent = clean(hops.map(function (h) { return h.name; }).join(' → ')); g.appendChild(t);
    g.addEventListener('click', function () { location.hash = route(['mesh', 'route', i]); });
    return g;
  }

  function routeDetail(d, i) {
    const r = arr(M.routes)[i];
    if (!r) { return add(d, el('p', 'empty', 'no such routed edge')); }
    const hops = routedHops(r);
    add(d, [el('h2', '', hops.map(function (h) { return h.name; }).join(' → ')),
      kv(hops.map(function (h) { return [h.kind, h.name]; }).concat([['basis', r.basis],
        ['connections', String(r.conns || 0)], ['endpoints', r.endpoints ? String(r.endpoints) : ''],
        ['map', r.origin], ['first', r.first], ['last', r.last]]))]);
    if (r.basis !== 'fused') {
      add(d, el('p', 'warn', 'completed from the route map: no tracer saw the hops past ' + r.from));
    }
    if (r.basis === 'service' && !r.pod && r.endpoints > 1) {
      add(d, el('p', 'muted', r.endpoints + ' endpoints: the traces cannot say which pod answered'));
    }
    const si = serviceIndex(r.from);
    add(d, el('p', '', ['client: ', si >= 0 ? link(['mesh', 'svc', si], r.from) : r.from]));
    add(d, [el('h3', '', 'route flags'), flagList(flagsOf(r))]);
  }

  // flagList links each flag to its detail.
  function flagList(fs) {
    const all = arr((M.health || {}).route_flags);
    return list(fs.map(function (f) { return link(['mesh', 'flag', all.indexOf(f)], flagText(f)); }));
  }

  function flagDetail(d, i) {
    const f = arr((M.health || {}).route_flags)[i];
    if (!f) { return add(d, el('p', 'empty', 'no such route flag')); }
    add(d, [el('h2', '', 'route flag ' + f.kind), kv([['subject', f.subject], ['detail', f.detail],
      ['connections', f.conns ? String(f.conns) : '']]),
    el('p', 'warn', 'the route map and the traces disagree here, or the map left it open')]);
    add(d, [el('h3', '', 'all route flags'), flagList(arr((M.health || {}).route_flags))]);
  }

  // routedList links each routed edge.
  function routedList() {
    return list(arr(M.routes).map(function (r, i) {
      return [link(['mesh', 'route', i], routedHops(r).map(function (h) { return h.name; }).join(' → ')), ' ' + routeNote(r)];
    }));
  }

  function serviceIndex(name) { return arr(M.services).findIndex(function (s) { return s.name === name; }); }

  function meshNode(name, p, sel) {
    const i = serviceIndex(name), s = arr(M.services)[i];
    let cls = 'node' + (s && arr(s.endpoints).length > 0 ? '' : ' client');
    if (s && !serviceShown(s)) { cls += ' dim'; }
    if (sel && sel.kind === 'svc' && sel.i === i) { cls += ' sel'; }
    const g = svg('g', {}, cls);
    g.appendChild(svg('rect', { x: p.x, y: p.y, width: 200, height: 36, rx: 4 }));
    g.appendChild(stext(p.x + 8, p.y + 15, short(name, 28)));
    g.appendChild(stext(p.x + 8, p.y + 29, s ? (s.requests || 0) + ' req · ' + (s.conns || 0) + ' conns' : '', 'start', 'label'));
    g.addEventListener('click', function () { if (i >= 0) { location.hash = route(['mesh', 'svc', i]); } });
    return g;
  }

  function meshEdge(e, i, pos, sel) {
    const a = pos[e.from], b = pos[e.to];
    const x1 = a.x + 200, y1 = a.y + 18, x2 = b.x, y2 = b.y + 18, mid = (x1 + x2) / 2;
    const d = x2 > x1 ? 'M' + x1 + ',' + y1 + ' C' + mid + ',' + y1 + ' ' + mid + ',' + y2 + ' ' + x2 + ',' + y2
      : 'M' + x1 + ',' + y1 + ' C' + (x1 + 80) + ',' + (y1 - 60) + ' ' + (x2 - 80) + ',' + (y2 - 60) + ' ' + x2 + ',' + y2;
    const state = ['fused', 'unresolved', 'mixed'].indexOf(e.state) >= 0 ? e.state : 'other';
    let cls = 'edge ' + state;
    if (!edgeShown(e)) { cls += ' dim'; }
    if (sel && sel.kind === 'edge' && sel.i === i) { cls += ' sel'; }
    const g = svg('g', {}, cls);
    const width = 1 + Math.min(4, Math.log2((e.conns || 0) + 1));
    g.appendChild(svg('path', { d: d, 'stroke-width': width.toFixed(1), 'marker-end': 'url(#arrow)' }));
    g.appendChild(svg('path', { d: d }, 'hit'));
    const t = svg('title', {}); t.textContent = clean(e.from + ' → ' + e.to + ' (' + e.state + ')'); g.appendChild(t);
    g.addEventListener('click', function () { location.hash = route(['mesh', 'edge', i]); });
    return g;
  }

  function edgeShown(e) {
    const a = arr(M.services)[serviceIndex(e.from)], b = arr(M.services)[serviceIndex(e.to)];
    return (!a || serviceShown(a)) || (!b || serviceShown(b));
  }

  function serviceDetail(d, i) {
    const s = arr(M.services)[i];
    if (!s) { return add(d, el('p', 'empty', 'no such service')); }
    add(d, [el('h2', '', s.name), kv([['named', s.named ? 'yes (naming rule)' : 'no'], ['TLS', s.tls || 'unknown'],
      ['connections', String(s.conns)], ['unresolved', String(s.unresolved)], ['requests', String(s.requests)],
      ['hosts', hostsOfService(s.name).join(', ')]])]);
    add(d, [el('h3', '', 'endpoints'), list(arr(s.endpoints).map(function (e) { return e; }))]);
    add(d, [el('h3', '', 'top paths'), table(['method', 'path', 'count'], arr(s.top_paths).map(function (p) {
      return { cells: [p.method, p.path, String(p.count)] };
    })), more(s.more_paths, 'paths')]);
    add(d, [el('h3', '', 'edges'), edgeList(function (e) { return e.from === s.name || e.to === s.name; })]);
    add(d, [el('h3', '', 'processes'), list(procLinks(function (p) { return p.service === s.name || arr(p.serves).indexOf(s.name) >= 0; }))]);
    add(d, [el('h3', '', 'conversations'), list(convLinks(function (c) { return arr(c.services).indexOf(s.name) >= 0; }))]);
  }

  // edgeList links each matching edge; following one walks the
  // connection to its other side (decision #11: "follow a connection
  // across hosts").
  function edgeList(pred) {
    const items = [];
    arr(M.edges).forEach(function (e, i) {
      if (pred(e)) { items.push([link(['mesh', 'edge', i], e.from + ' → ' + e.to), ' ' + e.state]); }
    });
    return list(items);
  }

  function edgeDetail(d, i) {
    const e = arr(M.edges)[i];
    if (!e) { return add(d, el('p', 'empty', 'no such edge')); }
    const fi = serviceIndex(e.from), ti = serviceIndex(e.to);
    add(d, [el('h2', '', e.from + ' → ' + e.to), kv([['state', e.state], ['connections', String(e.conns)],
      ['unresolved', String(e.unresolved)], ['first', e.first], ['last', e.last]])]);
    add(d, el('p', '', ['follow: ', fi >= 0 ? link(['mesh', 'svc', fi], 'client ' + e.from) : 'client ' + e.from,
      ' → ', ti >= 0 ? link(['mesh', 'svc', ti], 'server ' + e.to) : 'server ' + e.to]));
    if (e.state !== 'fused') {
      add(d, el('p', 'warn', 'part of this edge is unresolved: the far end was not traced (iormeshctl expand)'));
    }
    add(d, [el('h3', '', 'hops on this edge'), list(hopLinks(e))]);
  }

  // hopLinks are the request steps that cross edge e, each a link to
  // its step in the sequence view.
  function hopLinks(e) {
    const out = [];
    for (const c of arr(M.conversations)) {
      arr(c.steps).forEach(function (s, n) {
        if (s.kind === 'request' && s.service === e.from && s.to === e.to && convShown(c)) {
          out.push([link(['conv', c.id, 'step', n + 1], c.id + ' step ' + (n + 1)), ' ' + short(s.label, 50) + ' (' + s.ordering + ')']);
        }
      });
    }
    return out;
  }

  function list(items) {
    if (items.length === 0) { return el('p', 'empty', 'none'); }
    return el('ul', '', items.map(function (it) { return el('li', '', it); }));
  }

  function procLinks(pred) {
    const out = [];
    arr(M.processes).forEach(function (p, i) {
      if (pred(p)) { out.push(link(['proc', i], p.host + ' pid ' + p.pid + ' ' + (p.comm || ''))); }
    });
    return out;
  }

  function convLinks(pred) {
    return arr(M.conversations).filter(pred).map(function (c) {
      return [link(['conv', c.id], c.id + ' ' + short(c.label, 40)), ' ' + c.start];
    });
  }

  // ---- conversations and the sequence view -------------------------

  function renderConvs(view) {
    const cs = arr(M.conversations).filter(convShown);
    add(view, el('h2', '', 'Conversations — ' + cs.length + ' of ' + arr(M.conversations).length + ' shown'));
    add(view, table(['id', 'start', 'entry', 'request', 'hops', 'services'], cs.map(function (c) {
      return { cells: [link(['conv', c.id], c.id), c.start, c.entry, short(c.label, 50), String(c.hops),
        short(arr(c.services).join(' → '), 80)] };
    })));
    add(view, more(M.more_conversations, 'conversations (older, not in the model)'));
  }

  function convByID(id) { return arr(M.conversations).find(function (c) { return c.id === id; }); }

  const LANE = 190, ROW = 30, TOP = 64;

  function renderSequence(view, id, stepNo) {
    const c = convByID(id);
    if (!c) { return add(view, el('p', 'empty', 'no conversation ' + id)); }
    add(view, el('h2', '', 'Conversation ' + c.id + ' — ' + c.label + ' (' + c.hops + ' hops)'));
    const exp = el('p', 'muted', ['solid: causal · dashed: time-ordered · dotted: unordered · ']);
    if (/^[0-9]+$/.test(c.id)) {
      const a = el('a', '', 'static SVG'); a.href = 'sequence/' + c.id + '.svg'; exp.appendChild(a);
    }
    add(view, exp);
    const lanes = arr(c.lifelines), steps = arr(c.steps);
    const g = canvas(view, Math.max(1, lanes.length) * LANE, TOP + steps.length * ROW + 20);
    const bottom = TOP + steps.length * ROW;
    lanes.forEach(function (l, i) { g.appendChild(laneHead(l, i, bottom)); });
    steps.forEach(function (s, n) { g.appendChild(seqStep(c, s, n, stepNo === n + 1)); });
    add(view, more(c.more_steps, 'steps'));
  }

  function laneHead(l, i, bottom) {
    const x = i * LANE, g = svg('g', {}, 'lanehead' + (l.carrier ? ' carrier' : ''));
    g.appendChild(svg('line', { x1: x + LANE / 2, y1: 40, x2: x + LANE / 2, y2: bottom }, 'life'));
    g.appendChild(svg('rect', { x: x + 6, y: 2, width: LANE - 12, height: 38, rx: 3 }));
    g.appendChild(stext(x + LANE / 2, 17, short(l.service, 24), 'middle'));
    g.appendChild(stext(x + LANE / 2, 32, short(l.thread ? l.thread + (l.carrier ? ' (virtual)' : '') : l.host || '', 24), 'middle', 'label'));
    const si = storyIndex(l);
    if (si >= 0) { g.addEventListener('click', function () { location.hash = route(['thread', si]); }); }
    return g;
  }

  function storyIndex(l) {
    return arr(M.threads).findIndex(function (t) {
      return t.service === l.service && t.host === l.host && t.pid === l.pid &&
        t.pid_incarnation === l.pid_incarnation && t.tid === l.tid &&
        t.thread === l.thread && !!t.carrier === !!l.carrier;
    });
  }

  // seqStep draws step n: an arrow between two lifelines, or a file
  // note on its own; the ordering picks the line style.
  function seqStep(c, s, n, selected) {
    const y = TOP + n * ROW + 14, x1 = s.lane * LANE + LANE / 2;
    const ord = ['causal', 'time-ordered', 'unordered'].indexOf(s.ordering) >= 0 ? s.ordering : 'unordered';
    const g = svg('g', {}, 'step' + (selected ? ' sel' : ''));
    if (s.to_lane >= 0 && s.kind !== 'file') {
      const x2 = s.to_lane * LANE + LANE / 2;
      g.appendChild(svg('rect', { x: Math.min(x1, x2), y: y - 13, width: Math.max(Math.abs(x2 - x1), 4), height: 18 }, 'hit'));
      g.appendChild(svg('line', { x1: x1, y1: y, x2: x2, y2: y, 'marker-end': 'url(#arrow)' }, 'arrow ' + ord));
      const lbl = (n + 1) + ' ' + short(s.label, 34) + (ord === 'unordered' ? ' ?' : '');
      g.appendChild(stext((x1 + x2) / 2, y - 4, lbl, 'middle'));
    } else {
      const note = svg('g', {}, 'note');
      note.appendChild(svg('rect', { x: x1 + 6, y: y - 12, width: LANE - 20, height: 18, rx: 2 }));
      note.appendChild(stext(x1 + 10, y + 1, (n + 1) + ' ' + short(s.label, 26)));
      g.appendChild(svg('rect', { x: x1 + 6, y: y - 12, width: LANE - 20, height: 18 }, 'hit'));
      g.appendChild(note);
    }
    g.addEventListener('click', function () { location.hash = route(['conv', c.id, 'step', n + 1]); });
    return g;
  }

  function stepDetail(d, id, stepNo) {
    const c = convByID(id);
    if (!c) { return; }
    const s = arr(c.steps)[stepNo - 1];
    if (!s) {
      add(d, [el('h2', '', 'Conversation ' + c.id), kv([['start', c.start], ['entry', c.entry], ['request', c.label],
        ['hops', String(c.hops)], ['services', arr(c.services).join(' → ')]]),
      el('p', 'muted', 'click a step for its Java stack')]);
      return;
    }
    // A folded run (count > 1, task vg4): its bytes and latency are sums,
    // its stack and ref its first call's, and each label says so.
    const n = s.count > 1 ? s.count : 0;
    add(d, [el('h2', '', 'Step ' + stepNo + ': ' + s.kind), kv([['label', s.label], ['service', s.service],
      ['host', s.host], ['thread', s.thread ? s.thread + (s.carrier ? ' (virtual, on its carrier)' : '') : 'untraced'],
      ['to', s.to], [n ? 'bytes (' + n + ' calls)' : 'bytes', s.bytes ? String(s.bytes) : ''],
      [n ? 'latency (sum of ' + n + ' calls)' : 'latency', s.latency_ns ? dur(s.latency_ns) : ''],
      ['ordering', s.ordering], ['hop', s.hop]])]);
    add(d, [el('h3', '', n ? 'Java stack (first call)' : 'Java stack (top first)'), arr(s.stack).length ? el('ol', '', arr(s.stack).map(function (f) { return el('li', '', f); })) : el('p', 'empty', 'no Java frames')]);
    const peer = arr(c.lifelines)[s.to_lane];
    const si = storyIndex(arr(c.lifelines)[s.lane] || {}), pi = peer ? storyIndex(peer) : -1;
    add(d, el('p', '', [si >= 0 ? link(['thread', si], 'this thread’s story') : null,
      pi >= 0 ? ' · ' : null, pi >= 0 ? link(['thread', pi], 'peer thread’s story') : null]));
    if (s.ref) { refBox(d, s.ref, n ? 'raw event (first of ' + n + ')' : 'raw event'); }
    if (n && s.run_query) {
      add(d, kv([['last ref', s.last_ref]]));
      cmdBox(d, 'every call of the run', clean(s.run_query));
    }
  }

  // refBox shows the query command for the raw event: the static site
  // cannot run a query (docs/visualization.md "Detail on demand").
  function refBox(d, ref, title) {
    cmdBox(d, title, 'iormeshctl query -ref ' + shellQuote(clean(ref)));
  }

  // cmdBox shows a ready-made command under title with a copy button. A
  // folded run's query comes quoted from the model (viewmodel
  // runQuery: escaped globs, inert shell words, printable runes only,
  // so clean leaves it unchanged).
  function cmdBox(d, title, cmd) {
    const pre = el('pre', 'cmd', cmd);
    const msg = el('span', 'muted');
    add(d, [el('h3', '', title), pre, button('copy', function () { copy(cmd, pre, msg); }), ' ', msg]);
  }

  // shellQuote makes a ref one inert shell word: the command is meant to
  // be pasted into a shell, and the ref came from a server. clean has
  // already made control characters visible text.
  function shellQuote(s) {
    return /^[A-Za-z0-9._\/:@+=-]+$/.test(s) ? s : "'" + s.replace(/'/g, "'\\''") + "'";
  }

  // copy uses the clipboard when the browser grants it from file://,
  // else selects the text for Ctrl+C; a refusal never becomes a
  // console error.
  function copy(text, node, msg) {
    const select = function () {
      const r = document.createRange(); r.selectNodeContents(node);
      const sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(r);
      msg.textContent = 'selected — press Ctrl+C';
    };
    try {
      if (!navigator.clipboard) { select(); return; }
      navigator.clipboard.writeText(text).then(function () { msg.textContent = 'copied'; }, select);
    } catch (e) {
      select();
    }
  }

  // ---- threads, processes, health -----------------------------------

  function laneName(t) {
    return t.service + ' / ' + (t.thread || '?') + (t.carrier ? ' (virtual)' : '');
  }

  function renderThreads(view) {
    const rows = [];
    arr(M.threads).forEach(function (t, i) {
      if (storyShown(t)) {
        rows.push({ cells: [link(['thread', i], laneName(t)), t.host || '', String(t.pid || ''), String(t.tid || ''),
          String(arr(t.entries).length + (t.more_entries || 0))] });
      }
    });
    add(view, [el('h2', '', 'Threads — ' + rows.length + ' of ' + arr(M.threads).length + ' shown'),
      table(['service / thread', 'host', 'pid', 'tid', 'entries'], rows)]);
  }

  function renderThread(view, i) {
    const t = arr(M.threads)[i];
    if (!t) { return add(view, el('p', 'empty', 'no such thread')); }
    add(view, [el('h2', '', laneName(t)), el('p', 'muted', (t.host || '') + ' pid ' + t.pid + ' tid ' + t.tid +
      ' — ordered by this host’s clock only')]);
    add(view, more(t.more_entries, 'older entries'));
    add(view, table(['#', 'ordering', 'act', 'what', 'peer', 'step'], arr(t.entries).map(function (e, n) {
      return { cells: [String(n + 1), e.ordering, e.act, short(e.label, 60), e.peer || '',
        link(['conv', e.conversation, 'step', e.step], e.conversation + '/' + e.step)] };
    })));
    if (t.carrier) { add(view, el('p', 'warn', 'a virtual thread’s I/O named by its carrier: never causal')); }
  }

  function renderProcs(view) {
    const rows = [];
    arr(M.processes).forEach(function (p, i) {
      if (procShown(p)) {
        rows.push({ cells: [link(['proc', i], p.host + ' ' + p.pid), p.comm, p.app || '', p.service || '',
          String(p.incarnations > 1 ? p.incarnations + ' (restart)' : 1), String(arr(p.threads).length),
          String(arr(p.files).length), String(arr(p.sockets).length)] });
      }
    });
    add(view, [el('h2', '', 'Processes — ' + rows.length + ' of ' + arr(M.processes).length + ' shown'),
      partial(M.untracked), table(['host pid', 'comm', 'app', 'service', 'incarnations', 'threads', 'files', 'sockets'], rows),
      more(M.more_processes, 'processes')]);
  }

  // containerLabel is viewmodel.ContainerLabel (task jk4): "NAME (ID12)"
  // when the pod's container map names the container, the whole id when
  // it does not, '' outside a container.
  function containerLabel(p) {
    if (!p.container_id) { return ''; }
    if (!p.container) { return p.container_id; }
    return p.container + ' (' + p.container_id.slice(0, 12) + ')';
  }

  function renderProc(view, i) {
    const p = arr(M.processes)[i];
    if (!p) { return add(view, el('p', 'empty', 'no such process')); }
    add(view, [el('h2', '', p.host + ' pid ' + p.pid + ' ' + (p.comm || '')), kv([['fqdn', p.fqdn],
      ['project', p.project], ['container', containerLabel(p)], ['app', p.app], ['service', p.service],
      ['serves', arr(p.serves).join(', ')],
      ['incarnation', String(p.pid_incarnation) + ' of ' + p.incarnations], ['events', String(p.events)],
      ['java events', String(p.java_events)]])]);
    add(view, [el('h3', '', 'threads'), table(['name', 'tid', 'events', 'bytes', 'calls', 'carried (virtual threads)'],
      arr(p.threads).map(function (t) {
        return { cells: [t.name, String(t.tid), String(t.events), String(t.bytes), String(t.calls),
          t.carried || t.carried_calls ? t.carried + ' events, ' + t.carried_calls + ' calls' : ''] };
      })), more(p.more_threads, 'threads')]);
    add(view, [el('h3', '', 'files'), table(['path', 'read', 'written', 'ops'], arr(p.files).map(function (f) {
      return { cells: [f.path, String(f.read_bytes), String(f.write_bytes), arr(f.ops).join(', ') + (f.more_ops ? ' +' + f.more_ops : '')] };
    })), more(p.more_files, 'files')]);
    add(view, [el('h3', '', 'sockets'), table(['conn', 'fd', 'role', 'local → remote', 'service', 'peer', 'sent', 'received', 'requests'],
      arr(p.sockets).map(socketRow)), more(p.more_sockets, 'sockets')]);
  }

  function socketRow(s) {
    const si = serviceIndex(s.service);
    return { cells: [s.id, s.fd >= 0 ? String(s.fd) : '-', s.role, (s.local || '-') + ' → ' + (s.remote || '-'),
      si >= 0 ? link(['mesh', 'svc', si], s.service) : (s.service || '-'),
      s.peer_state === 'traced' ? arr(s.peers).join(', ') : 'untraced', String(s.sent_bytes), String(s.recv_bytes),
      String(s.requests)] };
  }

  function renderHealth(view) {
    const h = M.health || {}, r = M.run || {};
    add(view, [el('h2', '', 'Health — what the run could not see'), kv([['lost records', String(h.lost || 0)],
      ['skipped BPF runs', String(h.skipped_runs || 0)], ['unattributed calls', String(h.unattributed || 0)],
      ['attach gap', String(h.attach_gap || 0)], ['requests cut', String(h.requests_cut || 0)],
      ['fused / unresolved conns', (r.fused_conns || 0) + ' / ' + (r.unresolved_conns || 0)]])]);
    add(view, [el('h3', '', 'sessions'), table(['project', 'host', 'fqdn', 'id', 'segments', 'events', 'first', 'last', 'end'],
      arr(h.sessions).map(function (s) {
        return { cells: [s.project, s.host, s.fqdn, s.id, String(s.segments), String(s.events), s.first, s.last,
          s.end || 'no end recorded (running, or cut short)'] };
      }))]);
    add(view, [el('h3', '', 'clock bounds'), table(['host A', 'host B', 'windows', 'empty', 'one-sided', 'width'],
      arr(h.clocks).map(function (c) {
        return { cells: [c.host_a, c.host_b, String(c.windows), String(c.empty), String(c.one_sided),
          c.max_width_ns ? dur(c.min_width_ns) + '–' + dur(c.max_width_ns) : 'none'] };
      }))]);
    add(view, [el('h3', '', 'bad segments (left out)'), table(['segment', 'error'], arr(h.bad_segments).map(function (b) {
      return { cells: [b.name, b.error] };
    })), el('h3', '', 'ignored store entries'), list(arr(h.ignored))]);
    // Only a build with a route map has flags; a routeless run's health
    // screen stays as it was.
    const fs = arr(h.route_flags);
    if (fs.length) {
      add(view, [el('h3', '', 'route flags (route map vs traces)'), table(['kind', 'subject', 'detail', 'conns'],
        fs.map(function (f, i) {
          return { cells: [link(['mesh', 'flag', i], f.kind), f.subject, f.detail, f.conns ? String(f.conns) : '-'] };
        }))]);
    }
  }

  // ---- routing -----------------------------------------------------

  // parse splits "#/a/b" into decoded parts; a malformed escape yields
  // the mesh rather than an exception.
  function parse(hash) {
    try {
      return hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
    } catch (e) {
      return [];
    }
  }

  function render() {
    const parts = parse(location.hash);
    if (parts[0] === 'selftest') { selftest(); return; }
    show(parts);
  }

  // show renders one route into the view and the detail panel.
  function show(parts) {
    const view = document.getElementById('view'), d = document.getElementById('detail');
    view.replaceChildren(); d.replaceChildren();
    const top = ['mesh', 'convs', 'conv', 'threads', 'thread', 'procs', 'proc', 'health'].indexOf(parts[0]) >= 0 ? parts[0] : 'mesh';
    markTab(top);
    const n = Number(parts[2]);
    switch (top) {
      case 'mesh': renderMesh(view, parts[1] ? { kind: parts[1], i: n } : null); meshDetail(d, parts[1], n); break;
      case 'convs': renderConvs(view); add(d, el('p', 'muted', 'pick a conversation')); break;
      case 'conv': renderSequence(view, parts[1], Number(parts[3])); stepDetail(d, parts[1], Number(parts[3])); break;
      case 'threads': renderThreads(view); break;
      case 'thread': renderThread(view, Number(parts[1])); break;
      case 'procs': renderProcs(view); break;
      case 'proc': renderProc(view, Number(parts[1])); break;
      default: renderHealth(view);
    }
  }

  function meshDetail(d, kind, i) {
    if (kind === 'svc') { serviceDetail(d, i); return; }
    if (kind === 'edge') { edgeDetail(d, i); return; }
    if (kind === 'route') { routeDetail(d, i); return; }
    if (kind === 'flag') { flagDetail(d, i); return; }
    add(d, [el('h2', '', 'Edges'), edgeList(function () { return true; })]);
    if (arr(M.routes).length) { add(d, [el('h2', '', 'Routed edges'), routedList()]); }
    const fs = arr((M.health || {}).route_flags);
    if (fs.length) { add(d, [el('h2', '', 'Route flags'), flagList(fs)]); }
  }

  function markTab(top) {
    const on = { conv: 'convs', thread: 'threads', proc: 'procs' }[top] || top;
    for (const a of document.querySelectorAll('#tabs a')) { a.className = a.dataset.tab === on ? 'on' : ''; }
  }

  function buildHeader() {
    const r = M.run || {};
    document.getElementById('title').textContent = clean('iormesh · ' + arr(r.projects).join('+'));
    document.getElementById('run').textContent = clean(arr(r.hosts).length + ' hosts · ' + (r.sessions || 0) +
      ' sessions · ' + (r.events || 0) + ' events · ' + (r.first || '') + ' – ' + (r.last || '') +
      // A windowed build's window (task hk4); a full build has none.
      (r.window ? ' · window ' + r.window : '') +
      (M.broken ? ' · MODEL UNREADABLE: ' + M.broken : ''));
    const nav = document.getElementById('tabs');
    for (const t of tabs) { const a = link([t[0]], t[1]); a.dataset.tab = t[0]; nav.appendChild(a); }
    const g = el('a', '', 'graph.svg'); g.href = 'graph.svg'; nav.appendChild(g);
  }

  // selftest renders every route of the model in turn and reports the
  // ones that threw: what the headless-browser check reads
  // (internal/site browser_test.go), and a quick check by hand.
  function selftest() {
    const routes = [['mesh'], ['convs'], ['threads'], ['procs'], ['health']];
    arr(M.services).forEach(function (s, i) { routes.push(['mesh', 'svc', i]); });
    arr(M.edges).forEach(function (e, i) { routes.push(['mesh', 'edge', i]); });
    arr(M.routes).forEach(function (r, i) { routes.push(['mesh', 'route', i]); });
    arr((M.health || {}).route_flags).forEach(function (f, i) { routes.push(['mesh', 'flag', i]); });
    arr(M.threads).forEach(function (t, i) { routes.push(['thread', i]); });
    arr(M.processes).forEach(function (p, i) { routes.push(['proc', i]); });
    for (const c of arr(M.conversations)) {
      routes.push(['conv', c.id]);
      arr(c.steps).forEach(function (s, n) { routes.push(['conv', c.id, 'step', n + 1]); });
    }
    // Deep links past the lists, or not numbers at all (a pasted or
    // forged hash), must land on their "no such …" guard, not throw or
    // index a prototype.
    const guarded = [['mesh', 'route', 999], ['mesh', 'flag', -1], ['mesh', 'route', '__proto__']];
    const failed = [];
    for (const r of routes.concat(guarded)) {
      try { show(r.map(String)); } catch (e) { failed.push(r.join('/') + ': ' + e); }
    }
    for (const r of guarded) {
      show(r.map(String));
      if (document.getElementById('detail').textContent.indexOf('no such') < 0) { failed.push(r.join('/') + ': no guard shown'); }
    }
    // A folded run's detail must say its fields are sums or its first
    // call's (task vg4), and give the query for the whole run.
    for (const c of arr(M.conversations)) {
      arr(c.steps).forEach(function (s, n) {
        if (!(s.count > 1)) { return; }
        show(['conv', c.id, 'step', String(n + 1)]);
        const text = document.getElementById('detail').textContent;
        ['bytes (' + s.count + ' calls)', 'latency (sum of ' + s.count + ' calls)', 'Java stack (first call)',
          'raw event (first of ' + s.count + ')', 'every call of the run'].forEach(function (w) {
          if (text.indexOf(w) < 0) { failed.push(['conv', c.id, 'step', n + 1].join('/') + ': lacks ' + w); }
        });
      });
    }
    const shownByHost = hostFilterList(failed);
    routes.push.apply(routes, guarded);
    const out = el('pre', '', failed.length ? 'selftest: FAILED\n' + failed.join('\n') : 'selftest: ok ' + routes.length + ' routes');
    out.id = 'selftest';
    document.getElementById('view').replaceChildren(out, shownByHost);
  }

  // hostFilterList selects each host of the dropdown (a store host
  // directory) in turn and lists, one item per host by its index, what
  // the filter shows: conversation IDs, thread and service indices.
  // The browser test compares the list with the model's expected one,
  // pinned by hand (site_test.go hostFilterWant) rather than restated
  // here. A host the run does not have must show nothing. It restores
  // the filter after.
  function hostFilterList(failed) {
    const saved = filter.host;
    const box = el('ul');
    box.id = 'hostfilter';
    arr(M.run && M.run.hosts).forEach(function (h, i) {
      filter.host = h;
      const s = shownOnHost();
      add(box, el('li', '', 'host ' + i + ': convs ' + s.convs + '; threads ' + s.threads + '; services ' + s.services));
    });
    filter.host = '\u0000no such host';
    const none = shownOnHost();
    if (none.convs || none.threads || none.services) {
      failed.push('filter host not in the run shows: ' + JSON.stringify(none));
    }
    filter.host = saved;
    return box;
  }

  // shownOnHost is what the current filter shows: the conversations'
  // IDs and the threads' and services' indices, space-separated.
  function shownOnHost() {
    const idx = function (list, shown) {
      return arr(list).map(function (x, i) { return shown(x) ? String(i) : ''; }).filter(Boolean).join(' ');
    };
    return {
      convs: arr(M.conversations).filter(convShown).map(function (c) { return c.id; }).join(' '),
      threads: idx(M.threads, storyShown), services: idx(M.services, serviceShown),
    };
  }

  buildHeader();
  buildFilters();
  window.addEventListener('hashchange', render);
  render();
})();
