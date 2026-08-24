/* Grok Pane UI. Talks to the pane server over HTTP + WS. ACP stays in Go. */
(function () {
  (function hoistWorkspace() {
    var shell = document.getElementById('shell');
    var rail = document.getElementById('rail');
    var ws = document.getElementById('workspace');
    if (!shell || !rail || !ws) return;
    if (rail.parentNode !== shell) shell.insertBefore(rail, shell.firstChild);
    if (ws.parentNode !== shell) shell.appendChild(ws);
  })();

  function currentTheme() {
    return document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light';
  }

  function escapeText(s) {
    return String(s || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  function renderMd(src) {
    var raw = String(src || '');
    var html;
    try {
      var lib = window.marked;
      var parse = lib && (typeof lib.parse === 'function' ? lib.parse : (typeof lib.marked === 'function' ? lib.marked : (typeof lib === 'function' ? lib : null)));
      if (parse) {
        html = parse.call(lib, raw, { gfm: true, breaks: true });
      } else {
        html = '<p>' + escapeText(raw).replace(/\n/g, '<br>') + '</p>';
      }
    } catch (e) {
      html = '<p>' + escapeText(raw) + '</p>';
    }
    // marked hands raw HTML straight through, so everything below is
    // agent-authored markup rendered in the same document as the
    // permission card. `style`/`class`/`id` would let it restyle or hide
    // that card, and an `img` fetches an attacker URL the moment it
    // renders — neither is worth any markdown feature we lose.
    if (window.DOMPurify && DOMPurify.isSupported) {
      return DOMPurify.sanitize(html, {
        USE_PROFILES: { html: true },
        ADD_ATTR: ['target', 'rel'],
        FORBID_TAGS: ['style', 'form', 'input', 'button', 'img', 'base'],
        FORBID_ATTR: ['style', 'class', 'id', 'srcset', 'src']
      });
    }
    // No usable sanitizer (asset dropped, or a WebView DOMPurify does not
    // support): show the markdown as text rather than trust it.
    return '<p>' + escapeText(raw).replace(/\n/g, '<br>') + '</p>';
  }

  function openExternal(href) {
    if (!href) return;
    if (window.runtime && typeof window.runtime.BrowserOpenURL === 'function') {
      window.runtime.BrowserOpenURL(href);
      return;
    }
    var api = desktopAPI();
    if (api && typeof api.OpenURL === 'function') {
      Promise.resolve(api.OpenURL(href));
      return;
    }
    window.open(href, '_blank', 'noopener,noreferrer');
  }

  // Schemes a click may hand to the operating system. DOMPurify lets more
  // through (ftp, xmpp, cid, callto), but nothing on this side knows what
  // to do with those; tel: and sms: are here because the mobile shells
  // forward them and a phone number in agent markdown should still dial.
  var externalSchemes = ['http:', 'https:', 'mailto:', 'tel:', 'sms:'];

  function isDesktop() {
    return !!(window.runtime || window.wails || (window.go && window.go.main));
  }

  function fetchJSON(url, tries, opts) {
    tries = tries == null ? 16 : tries;
    return authFetch(url, opts).then(function (r) {
      if (!r.ok) throw new Error(String(r.status));
      return r.json();
    }).catch(function (err) {
      if (tries <= 1) throw err;
      return new Promise(function (resolve, reject) {
        setTimeout(function () {
          fetchJSON(url, tries - 1, opts).then(resolve, reject);
        }, 400);
      });
    });
  }

  // The token pane put in the page, or the one the desktop app read off
  // disk. Empty is normal for a remote pane reached over the tailnet,
  // where Tailscale identity is the credential instead.
  var paneToken = (function () {
    try {
      var m = document.querySelector('meta[name="pane-token"]');
      return (m && m.content) || '';
    } catch (e) { return ''; }
  })();

  function setPaneToken(t) {
    paneToken = String(t || '');
  }

  // The token belongs to one pane: the one that served this page, or the
  // local one the desktop app read it from. `?pane=` and the stored
  // pane-url can point everything at another host, and handing this to a
  // host of someone else's choosing would give away the credential.
  function ownsToken(target) {
    var u;
    try { u = new URL(target, location.href); } catch (e) { return false; }
    // Compare hosts, not origins: ws:// and http:// to the same server
    // are the same pane, but URL.origin says otherwise.
    if (u.host === location.host) return true;
    if (!isDesktop()) return false;
    var h = u.hostname;
    return h === '127.0.0.1' || h === 'localhost' || h === '::1' || h === '[::1]';
  }

  // Same-origin calls carry the token in a header; a WebSocket cannot set
  // headers, so /ws takes it as a query parameter instead.
  function authFetch(url, opts) {
    opts = opts || {};
    if (paneToken && ownsToken(url)) {
      var h = opts.headers || {};
      h['X-Pane-Token'] = paneToken;
      opts.headers = h;
    }
    return fetch(url, opts);
  }

  function paneHTTP() {
    var q = new URLSearchParams(location.search).get('pane');
    if (q) return q.replace(/\/$/, '');
    try {
      var s = localStorage.getItem('pane-url');
      if (s) return s.replace(/\/$/, '');
    } catch (e) {}
    if (window.__paneOrigin) return String(window.__paneOrigin).replace(/\/$/, '');
    if (isDesktop()) return 'http://127.0.0.1:7420';
    return location.origin;
  }

  function paneWS(cwd, sid, replay) {
    var http = paneHTTP();
    var u;
    try { u = new URL(http); } catch (e) { u = new URL('http://127.0.0.1:7420'); }
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
    u.pathname = '/ws';
    u.search = '';
    if (cwd) u.searchParams.set('cwd', cwd);
    if (sid) u.searchParams.set('sid', sid);
    if (replay) u.searchParams.set('replay', '1');
    var model = stored('pane-model');
    var effort = stored('pane-effort');
    if (model) u.searchParams.set('model', model);
    if (effort) u.searchParams.set('effort', effort);
    if (paneToken && ownsToken(u.toString())) u.searchParams.set('t', paneToken);
    return u.toString();
  }

  function stored(k) {
    try { return localStorage.getItem(k) || ''; } catch (e) { return ''; }
  }
  function store(k, v) {
    try { if (v) localStorage.setItem(k, v); } catch (e) {}
  }
  function forget(k) {
    try { localStorage.removeItem(k); } catch (e) {}
  }

  var themeBtn = document.getElementById('theme');
  var thoughtsBtn = document.getElementById('thoughts');
  var autoScrollBtn = document.getElementById('autoscroll');
  var status = document.getElementById('status');
  var liveEl = document.getElementById('live');
  var cwdEl = document.getElementById('cwd');
  var input = document.getElementById('in');
  var sendBtn = document.getElementById('send');
  var queueEl = document.getElementById('queue');
  var modelEl = document.getElementById('model');
  var effortEl = document.getElementById('effort');
  var usageEl = document.getElementById('usage');
  var usageRing = document.getElementById('usage-ring');
  var usagePop = document.getElementById('usage-pop');
  var usageWrap = document.querySelector('.usage-wrap');
  var attachBtn = document.getElementById('attach');
  var fileInput = document.getElementById('file');
  var chipsEl = document.getElementById('chips');
  var dropEl = document.getElementById('drop');
  var pending = [];
  var logRoot = document.getElementById('log');
  var jumpBtn = document.getElementById('jump-bottom');
  var sessionsEl = document.getElementById('sessions');
  var projectsEl = document.getElementById('projects');
  var diskSessions = [];
  var remoteEl = document.getElementById('remote');
  var projectBtn = document.getElementById('project');
  var changeBtn = document.getElementById('change-project');
  var newBtn = document.getElementById('new-session');
  var showThoughts = false;
  var autoScroll = stored('pane-autoscroll') !== '0';
  var project = '';
  var sessions = [];
  var active = null;
  var n = 0;
  var pickingProject = false;

  function setPickingProject(on) {
    pickingProject = !!on;
    syncRailChrome();
  }

  // The rail refuses while a turn is running, so it has to look refused —
  // a button that silently does nothing reads as a broken app.
  function syncRailChrome() {
    if (!changeBtn) return;
    var locked = railLocked();
    changeBtn.disabled = pickingProject || locked;
    changeBtn.setAttribute('aria-busy', pickingProject ? 'true' : 'false');
    changeBtn.title = locked ? 'Finish the current turn first' : 'Choose a different folder';
  }

  function paintTheme() {
    themeBtn.textContent = currentTheme() === 'dark' ? 'Light' : 'Dark';
  }

  function paintThoughtsBtn() {
    thoughtsBtn.setAttribute('aria-pressed', showThoughts ? 'true' : 'false');
    thoughtsBtn.textContent = 'Thoughts';
    thoughtsBtn.title = showThoughts ? 'Showing reasoning — click to hide' : 'Reasoning hidden — click to show';
  }
  paintTheme();
  themeBtn.addEventListener('click', function () {
    var next = currentTheme() === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    var meta = document.querySelector('meta[name="color-scheme"]');
    if (meta) meta.content = next;
    try { localStorage.setItem('pane-theme', next); } catch (e) {}
    paintTheme();
  });
  function paintAutoScrollBtn() {
    if (!autoScrollBtn) return;
    autoScrollBtn.setAttribute('aria-pressed', autoScroll ? 'true' : 'false');
    autoScrollBtn.textContent = 'Follow';
    autoScrollBtn.title = autoScroll ? 'Following new output — click to stop' : 'Not following — click to follow';
  }
  function bindHeaderToggle(el, fn) {
    if (!el) return;
    el.addEventListener('pointerdown', function (e) {
      e.stopPropagation();
    });
    el.addEventListener('click', function (e) {
      e.preventDefault();
      e.stopPropagation();
      fn();
    });
  }
  paintThoughtsBtn();
  bindHeaderToggle(thoughtsBtn, function () {
    showThoughts = !showThoughts;
    paintThoughtsBtn();
    sessions.forEach(function (s) { s.showThoughts(showThoughts); });
  });
  paintAutoScrollBtn();
  bindHeaderToggle(autoScrollBtn, function () {
    autoScroll = !autoScroll;
    try { localStorage.setItem('pane-autoscroll', autoScroll ? '1' : '0'); } catch (e) {}
    paintAutoScrollBtn();
    if (autoScroll && active) active.scroll(true);
    else syncJump();
  });

  function setStatus(text, cls) {
    text = text || '';
    cls = cls || '';
    status.textContent = text;
    status.className = cls;
    if (liveEl) {
      liveEl.hidden = !text || (cls === 'ok' && text === 'ready');
    }
  }

  function canSend() {
    var has = !!(input.value.replace(/\s+$/, '') || pending.length > 0);
    if (!has) return false;
    if (active && !active.dead && active.ws && active.ws.readyState === 1) return true;
    return !!project;
  }

  function syncSend() {
    var ok = canSend();
    sendBtn.disabled = !ok;
    sendBtn.textContent = (active && active.busy) ? 'Queue' : 'Send';
    input.placeholder = (active && active.busy) ? 'Queue a follow-up…' : 'Message…';
  }

  function syncBusyChrome() {
    var on = !!(active && active.busy);
    document.documentElement.dataset.busy = on ? 'true' : 'false';
    document.documentElement.setAttribute('aria-busy', on ? 'true' : 'false');
    syncSend();
    syncRailChrome();
    refreshSessionRow(active);
    paintQueue();
  }

  function setBusy(on) {
    if (active) active.busy = on;
    syncBusyChrome();
  }

  // `busy` covers both "a turn is running" and "the socket is down", but
  // only the first is a reason to refuse: a tab whose pane went away must
  // still be closable and must not pin the whole rail.
  function turnInFlight(s) {
    return !!(s && s.busy && s.live);
  }

  function railLocked() {
    return turnInFlight(active);
  }

  function grow() {
    input.style.height = 'auto';
    input.style.height = Math.min(input.scrollHeight, 144) + 'px';
  }

  function normPath(p) {
    p = String(p || '').replace(/\\/g, '/').replace(/\/+$/, '');
    return p;
  }

  function samePath(a, b) {
    return normPath(a) === normPath(b);
  }

  function basename(p) {
    if (!p) return '';
    var parts = normPath(p).split('/');
    return parts[parts.length - 1] || p;
  }

  function looksLikeSessionID(s) {
    return /^01[0-9a-fA-F-]{20,}$/.test(String(s || ''));
  }

  function paintCwd(path) {
    if (!path) {
      cwdEl.hidden = true;
      cwdEl.textContent = '';
      return;
    }
    cwdEl.hidden = false;
    cwdEl.title = 'copy ' + path;
    cwdEl.textContent = '';
    var span = document.createElement('span');
    span.className = 'cwd-path';
    span.textContent = path;
    cwdEl.appendChild(span);
  }

  var renaming = null;
  var renamingProject = null;
  var projectNames = {};
  var projectListCache = [];
  var composerHistIdx = -1;
  var composerHold = '';
  var composerHistApplying = false;

  function projectLabel(cwd) {
    return projectNames[normPath(cwd)] || basename(cwd);
  }

  function commitRename(s, val) {
    val = String(val || '').replace(/\s+/g, ' ').trim();
    if (val) {
      var id = s.id || s.resumeID;
      var cwd = s.cwd || project;
      s.title = val;
      s.named = true;
      if (id && cwd) {
        authFetch(paneHTTP() + '/v1/rename?cwd=' + encodeURIComponent(cwd) + '&id=' + encodeURIComponent(id), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ title: val })
        }).catch(function () {});
      }
      if (s.disk) {
        diskSessions.forEach(function (h) {
          if (h && h.id === id) h.title = val;
        });
      }
    }
    renaming = null;
    sessPaintKey = '';
    paintSessions();
  }

  function startRename(s) {
    if (!s || renaming === s) return;
    renaming = s;
    sessPaintKey = '';
    paintSessions();
  }

  function highlightActiveSession() {
    if (!sessionsEl) return;
    var buttons = sessionsEl.querySelectorAll('.sess');
    for (var i = 0; i < buttons.length; i++) {
      var lid = +buttons[i].getAttribute('data-lid');
      buttons[i].classList.toggle('active', !!(active && active.localId === lid));
    }
  }

  function refreshSessionRow(s) {
    if (!s || !sessionsEl) return;
    var btn = sessionsEl.querySelector('.sess[data-lid="' + s.localId + '"]');
    if (!btn) {
      paintSessions();
      return;
    }
    btn.textContent = s.title + (s.busy ? ' ·' : '') + (s.queue && s.queue.length ? ' +' + s.queue.length : '');
    btn.title = (s.busy ? 'working… — ' : '') + (s.queue && s.queue.length ? s.queue.length + ' queued — ' : '') + 'double-click to rename';
    if (btn.parentNode) btn.parentNode.classList.toggle('locked', !!s.busy);
    highlightActiveSession();
  }

  var sessPaintKey = '';

  function paintSessions() {
    if (renaming && sessionsEl.querySelector('.sess-edit')) return;
    var key = sessions.map(function (s) {
      return s.localId + '\t' + s.title + '\t' + (s.busy ? '1' : '0') + '\t' + ((s.queue && s.queue.length) || 0);
    }).join('|') + '//' + diskSessions.map(function (h) { return (h.id || '') + '\t' + (h.title || ''); }).join('|');
    if (key === sessPaintKey && sessionsEl.firstChild && !renaming) {
      highlightActiveSession();
      return;
    }
    sessPaintKey = key;
    sessionsEl.textContent = '';
    var seen = {};
    var any = false;
    sessions.forEach(function (s) {
      if (project && s.cwd && !samePath(s.cwd, project)) return;
      var sid = s.id || s.resumeID;
      if (sid) seen[sid] = true;
      any = true;
      var row = document.createElement('div');
      row.className = 'sess-row' + (s.busy ? ' locked' : '');
      if (renaming === s) {
        var inp = document.createElement('input');
        inp.type = 'text';
        inp.className = 'sess-edit';
        inp.value = s.title;
        inp.setAttribute('aria-label', 'Session name');
        // Escape has to clear the paint key too, or the repaint below
        // short-circuits on an unchanged key and the box stays up — and
        // the blur it eventually gets would apply the cancelled name.
        var cancelled = false;
        inp.addEventListener('keydown', function (e) {
          if (e.key === 'Enter') {
            e.preventDefault();
            commitRename(s, inp.value);
          }
          if (e.key === 'Escape') {
            e.preventDefault();
            cancelled = true;
            renaming = null;
            sessPaintKey = '';
            paintSessions();
          }
          e.stopPropagation();
        });
        inp.addEventListener('blur', function () {
          if (cancelled) return;
          commitRename(s, inp.value);
        });
        row.appendChild(inp);
        sessionsEl.appendChild(row);
        setTimeout(function () {
          inp.focus();
          inp.select();
        }, 0);
        return;
      }
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'sess' + (s === active ? ' active' : '');
      b.setAttribute('data-lid', String(s.localId));
      b.textContent = s.title + (s.busy ? ' ·' : '') + (s.queue && s.queue.length ? ' +' + s.queue.length : '');
      b.title = (s.busy ? 'working… — ' : '') + (s.queue && s.queue.length ? s.queue.length + ' queued — ' : '') + 'double-click to rename';
      b.addEventListener('click', function () {
        if (s === active) {
          startRename(s);
          return;
        }
        activate(s);
      });
      b.addEventListener('dblclick', function (ev) {
        ev.preventDefault();
        ev.stopPropagation();
        startRename(s);
      });
      var x = document.createElement('button');
      x.type = 'button';
      x.className = 'sess-close';
      x.title = 'Delete session permanently';
      x.textContent = '×';
      x.addEventListener('click', function (ev) {
        ev.stopPropagation();
        wipeSession(s.cwd || project, s.id || s.resumeID, s.title, s);
      });
      row.appendChild(b);
      row.appendChild(x);
      sessionsEl.appendChild(row);
    });
    diskSessions.forEach(function (h) {
      if (!h || !h.id || seen[h.id]) return;
      any = true;
      var row = document.createElement('div');
      row.className = 'sess-row';
      if (renaming && renaming.disk && renaming.id === h.id) {
        // Hold the row being renamed: reading the module-level `renaming`
        // from these handlers throws once Escape has cleared it.
        var target = renaming;
        var cancelled2 = false;
        var inp2 = document.createElement('input');
        inp2.type = 'text';
        inp2.className = 'sess-edit';
        inp2.value = h.title || h.id;
        inp2.setAttribute('aria-label', 'Session name');
        inp2.addEventListener('keydown', function (e) {
          if (e.key === 'Enter') {
            e.preventDefault();
            commitRename(target, inp2.value);
          }
          if (e.key === 'Escape') {
            e.preventDefault();
            cancelled2 = true;
            renaming = null;
            sessPaintKey = '';
            paintSessions();
          }
          e.stopPropagation();
        });
        inp2.addEventListener('blur', function () {
          if (cancelled2) return;
          commitRename(target, inp2.value);
        });
        row.appendChild(inp2);
        sessionsEl.appendChild(row);
        setTimeout(function () {
          inp2.focus();
          inp2.select();
        }, 0);
        return;
      }
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'sess';
      b.textContent = h.title || h.id;
      var when = (h.updated || '').replace('T', ' ').replace(/\.\d+Z$/, 'Z');
      b.title = (h.id || '') + (when ? '\n' + when : '') + '\ndouble-click to rename';
      b.addEventListener('click', function () {
        newSession(h.cwd || project, resumeOpts(h));
      });
      b.addEventListener('dblclick', function (ev) {
        ev.preventDefault();
        ev.stopPropagation();
        startRename({ disk: true, id: h.id, cwd: h.cwd || project, title: h.title || h.id });
      });
      var x = document.createElement('button');
      x.type = 'button';
      x.className = 'sess-close';
      x.title = 'Delete session permanently';
      x.textContent = '×';
      x.addEventListener('click', function (ev) {
        ev.stopPropagation();
        wipeSession(h.cwd || project, h.id, h.title || h.id, null);
      });
      row.appendChild(b);
      row.appendChild(x);
      sessionsEl.appendChild(row);
    });
    if (!any) {
      var empty = document.createElement('div');
      empty.className = 'hist';
      empty.style.cursor = 'default';
      empty.textContent = 'no sessions';
      sessionsEl.appendChild(empty);
    }
  }

  function socketLive(s) {
    var st = s && s.ws && s.ws.readyState;
    return st === 0 || st === 1;
  }

  function ensureConnected(s) {
    if (!s || s.dead) return;
    if (socketLive(s)) return;
    s.giveUp = false;
    s.connect();
  }

  function kickReconnects() {
    sessions.forEach(function (s) {
      if (s.dead) return;
      s.giveUp = false;
      if (s.reconnects > 3) s.reconnects = 3;
      ensureConnected(s);
    });
  }

  function activate(s) {
    if (!s) return;
    if (s === active) {
      ensureConnected(s);
      input.focus();
      return;
    }
    active = s;
    sessions.forEach(function (x) {
      if (x.el) x.el.classList.toggle('active', x === s);
    });
    highlightActiveSession();
    paintCwd(s.cwd);
    setStatus(s.statusText || (s.busy ? 'working…' : 'ready'), s.statusCls || (s.busy ? 'busy' : 'ok'));
    document.documentElement.dataset.busy = s.busy ? 'true' : 'false';
    document.documentElement.setAttribute('aria-busy', s.busy ? 'true' : 'false');
    syncSend();
    syncRailChrome();
    paintQueue();
    paintCatalog();
    paintUsage();
    syncJump();
    ensureConnected(s);
    input.focus();
  }

  function Session(cwd) {
    this.localId = ++n;
    this.id = '';
    this.cwd = cwd || project || '';
    this.title = 'Session ' + this.localId;
    this.named = false;
    this.resumeID = '';
    this.seenReady = false;
    this.dead = false;
    this.busy = true;
    // `live` is the socket half of `busy`: true only between a ready
    // handshake and the close that follows it.
    this.live = false;
    this.statusText = 'connecting…';
    this.statusCls = '';
    this.reconnects = 0;
    this.retry = 0;
    this.handshakeSince = 0;
    this.giveUp = false;
    this.startedReply = false;
    this.tools = {};
    this.ws = null;
    this.agentBuf = '';
    this.agentEl = null;
    this.thoughtBuf = '';
    this.thoughtEl = null;
    this.queue = [];
    this.model = stored('pane-model');
    this.effort = stored('pane-effort');
    this.models = [];
    this.used = 0;
    this.context = 0;
    this.usage = null;
    this.talked = false;
    this.toolsBox = null;
    this.toolsList = null;
    this.toolsSum = null;
    this.toolCount = 0;
    this.askEl = null;
    this.asking = false;
    this.askKey = '';
    this.warned = false;
    this.stick = true;
    this.el = document.createElement('div');
    this.el.className = 'log-slot';
    this.el.setAttribute('role', 'log');
    var slot = this;
    this.el.addEventListener('scroll', function () {
      if (slot.pinning) return;
      slot.stick = nearBottom(slot.el);
      if (slot === active) syncJump();
    }, { passive: true });
    logRoot.appendChild(this.el);
  }

  function nearBottom(el) {
    if (!el) return true;
    return el.scrollHeight - el.scrollTop - el.clientHeight < 48;
  }

  function syncJump() {
    if (!jumpBtn) return;
    jumpBtn.hidden = !(active && active.el && !nearBottom(active.el));
  }

  Session.prototype.scroll = function (force) {
    if (!force && (!autoScroll || !this.stick)) {
      if (this === active) syncJump();
      return;
    }
    this.stick = true;
    this.pinning = true;
    this.el.scrollTop = this.el.scrollHeight;
    this.pinning = false;
    if (this === active) syncJump();
  };

  Session.prototype.addYou = function (text, files) {
    this.toolsBox = null;
    this.toolsList = null;
    this.toolsSum = null;
    this.toolCount = 0;
    if (!this.talked) {
      this.talked = true;
      syncNewSession();
    }
    var wrap = document.createElement('div');
    wrap.className = 'msg you';
    var who = document.createElement('div');
    who.className = 'who';
    who.textContent = 'you';
    wrap.appendChild(who);
    if (text) {
      var body = document.createElement('div');
      body.className = 'body';
      body.textContent = text;
      wrap.appendChild(body);
    }
    if (files && files.length) {
      var row = document.createElement('div');
      row.className = 'you-files';
      files.forEach(function (f) {
        var chip = document.createElement('div');
        chip.className = 'you-file';
        if (f.preview || (f.mime && f.mime.indexOf('image/') === 0)) {
          var img = document.createElement('img');
          img.alt = '';
          img.src = f.preview || '';
          if (!img.src && f.path) img.alt = f.name || 'image';
          if (img.src) chip.appendChild(img);
        }
        var lab = document.createElement('span');
        lab.textContent = f.name || basename(f.path) || 'file';
        chip.appendChild(lab);
        row.appendChild(chip);
      });
      wrap.appendChild(row);
    }
    this.el.appendChild(wrap);
    this.agentBuf = '';
    this.agentEl = null;
    if (autoScroll) this.scroll(true);
    else syncJump();
  };

  Session.prototype.addOut = function (text) {
    if (!text) return;
    if (!this.agentEl) {
      var wrap = document.createElement('div');
      wrap.className = 'msg agent';
      var who = document.createElement('div');
      who.className = 'who';
      who.textContent = 'grok';
      var body = document.createElement('div');
      body.className = 'body md';
      wrap.appendChild(who);
      wrap.appendChild(body);
      this.el.appendChild(wrap);
      this.agentEl = body;
      this.agentBuf = '';
    }
    this.agentBuf += text;
    this.agentEl.innerHTML = renderMd(this.agentBuf);
    this.scroll();
  };

  Session.prototype.addThought = function (text) {
    if (!text) return;
    this.thoughtBuf += text;
    if (!showThoughts) return;
    if (!this.thoughtEl) {
      var wrap = document.createElement('div');
      wrap.className = 'msg thought';
      var who = document.createElement('div');
      who.className = 'who';
      who.textContent = 'thoughts';
      var body = document.createElement('div');
      body.className = 'body';
      wrap.appendChild(who);
      wrap.appendChild(body);
      if (this.agentEl && this.agentEl.parentNode) {
        this.el.insertBefore(wrap, this.agentEl.parentNode);
      } else {
        this.el.appendChild(wrap);
      }
      this.thoughtEl = body;
    }
    this.thoughtEl.textContent = this.thoughtBuf;
    this.scroll();
  };

  Session.prototype.showThoughts = function (on) {
    var existing = this.el.querySelector('.msg.thought');
    if (!on) {
      if (existing) existing.hidden = true;
      return;
    }
    if (!this.thoughtBuf) return;
    if (existing) {
      existing.hidden = false;
      var body = existing.querySelector('.body');
      if (body) body.textContent = this.thoughtBuf;
      this.thoughtEl = body;
      this.scroll();
      return;
    }
    var saved = this.thoughtBuf;
    this.thoughtBuf = '';
    this.thoughtEl = null;
    this.addThought(saved);
  };

  Session.prototype.addTool = function (title) {
    title = String(title || 'tool');
    if (!this.toolsBox) {
      var box = document.createElement('details');
      box.className = 'msg tools';
      var sum = document.createElement('summary');
      var list = document.createElement('div');
      list.className = 'tool-list';
      box.appendChild(sum);
      box.appendChild(list);
      this.el.appendChild(box);
      this.toolsBox = box;
      this.toolsList = list;
      this.toolsSum = sum;
      this.toolCount = 0;
    }
    this.toolCount++;
    var row = document.createElement('div');
    row.textContent = title;
    this.toolsList.appendChild(row);
    this.toolsSum.textContent = this.toolCount === 1 ? title : (this.toolCount + ' tools · ' + title);
    this.scroll();
  };

  Session.prototype.addAsk = function (msg) {
    var s = this;
    var questions = (msg && msg.questions) || [];
    if (!questions.length) return;
    var key;
    try { key = JSON.stringify(questions); } catch (e) { key = String(questions.length); }
    // The same question can arrive more than once for one tool call.
    // Rebuilding the card would throw away an answer already given and
    // wipe anything typed into the free-text box. The key is cleared at
    // the start of every turn, so a repeat next turn still shows.
    if (key === s.askKey && s.askEl && s.askEl.parentNode) return;
    s.askKey = key;
    if (s.askEl && s.askEl.parentNode) {
      s.askEl.parentNode.removeChild(s.askEl);
    }
    s.asking = true;
    var wrap = document.createElement('div');
    wrap.className = 'msg ask';
    var who = document.createElement('div');
    who.className = 'who';
    who.textContent = 'grok asks';
    wrap.appendChild(who);
    var chosen = questions.map(function () { return []; });
    var free = questions.map(function () { return ''; });
    var multiFlags = [];
    questions.forEach(function (q, qi) {
      var block = document.createElement('div');
      block.className = 'ask-q';
      var title = document.createElement('div');
      title.className = 'ask-title';
      title.textContent = q.question || q.header || 'Question';
      block.appendChild(title);
      var opts = document.createElement('div');
      opts.className = 'ask-opts';
      var multi = !!(q.multiSelect || q.multi_select);
      multiFlags[qi] = multi;
      var optionList = q.options || [];
      optionList.forEach(function (o, oi) {
        var label = (o && o.label) || (typeof o === 'string' ? o : '');
        if (!label) return;
        var b = document.createElement('button');
        b.type = 'button';
        b.className = 'ask-opt';
        var lab = document.createElement('div');
        lab.className = 'ask-label';
        lab.textContent = (oi + 1) + '. ' + label;
        b.appendChild(lab);
        if (o && o.description) {
          var d = document.createElement('div');
          d.className = 'ask-desc';
          d.textContent = o.description;
          b.appendChild(d);
        }
        b.addEventListener('click', function () {
          if (!s.asking) return;
          if (multi) {
            var i = chosen[qi].indexOf(label);
            if (i >= 0) chosen[qi].splice(i, 1);
            else chosen[qi].push(label);
            b.classList.toggle('on', i < 0);
            return;
          }
          chosen[qi] = [label];
          var siblings = opts.querySelectorAll('.ask-opt');
          for (var j = 0; j < siblings.length; j++) siblings[j].classList.toggle('on', siblings[j] === b);
          if (questions.length === 1) finish(collect(), 'accept');
        });
        opts.appendChild(b);
      });
      block.appendChild(opts);
      if (!optionList.length) {
        var inp = document.createElement('textarea');
        inp.className = 'ask-free';
        inp.rows = 2;
        inp.placeholder = 'Type an answer…';
        inp.addEventListener('input', function () { free[qi] = inp.value; });
        inp.addEventListener('keydown', function (e) {
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            e.stopPropagation();
            free[qi] = inp.value;
            var answers = collect();
            if (answers[qi] && answers[qi].selected.length) finish(answers, 'accept');
          }
        });
        block.appendChild(inp);
      }
      wrap.appendChild(block);
    });
    var actions = document.createElement('div');
    actions.className = 'ask-actions';
    var needsSubmit = questions.length > 1 || multiFlags.some(function (m) { return m; }) ||
      questions.some(function (q) { return !(q.options && q.options.length); });
    if (needsSubmit) {
      var submit = document.createElement('button');
      submit.type = 'button';
      submit.className = 'ask-submit';
      submit.textContent = 'Submit';
      submit.addEventListener('click', function () {
        if (!s.asking) return;
        var answers = collect();
        if (!answers.length || answers.some(function (a) { return !a.selected.length; })) return;
        finish(answers, 'accept');
      });
      actions.appendChild(submit);
    }
    var skip = document.createElement('button');
    skip.type = 'button';
    skip.className = 'ask-skip';
    skip.textContent = 'Skip';
    skip.addEventListener('click', function () {
      if (!s.asking) return;
      finish([], 'skip');
    });
    actions.appendChild(skip);
    wrap.appendChild(actions);
    s.el.appendChild(wrap);
    s.askEl = wrap;
    s.scroll();

    function collect() {
      return questions.map(function (q, i) {
        var sel = (chosen[i] || []).slice();
        var typed = String(free[i] || '').replace(/\s+$/, '');
        if (!sel.length && typed) sel = [typed];
        return { question: q.question || q.header || '', selected: sel };
      });
    }

    function finish(answers, action) {
      if (!s.asking) return;
      // Nothing was sent, so do not claim it was. Leave the card live so
      // the answer can be given again once the socket is back.
      if (!s.ws || s.ws.readyState !== 1) {
        s.setChrome('disconnected — answer not sent', 'err');
        return;
      }
      s.asking = false;
      // Answered. The server will not restate this question, so release
      // the key: if the agent genuinely asks the same thing again later
      // in the turn, that card must render.
      s.askKey = '';
      wrap.classList.add('done');
      var buttons = wrap.querySelectorAll('button');
      for (var i = 0; i < buttons.length; i++) buttons[i].disabled = true;
      s.ws.send(JSON.stringify({
        type: 'ask',
        action: action || 'accept',
        answers: answers || []
      }));
      var note = document.createElement('div');
      note.className = 'ask-picked';
      if (action === 'skip') {
        note.textContent = 'skipped';
      } else {
        var bits = [];
        (answers || []).forEach(function (a) {
          bits = bits.concat(a.selected || []);
        });
        note.textContent = bits.join(' · ') || 'answered';
      }
      wrap.appendChild(note);
      s.setChrome('working…', 'busy');
    }
  };

  Session.prototype.addPerm = function (msg) {
    var s = this;
    if (s.askEl && s.askEl.parentNode) {
      s.askEl.parentNode.removeChild(s.askEl);
    }
    // askEl now points at a permission card, so the ask dedupe can no
    // longer use it as evidence that the question is still on screen.
    s.askKey = '';
    s.asking = true;
    var wrap = document.createElement('div');
    wrap.className = 'msg ask perm';
    var who = document.createElement('div');
    who.className = 'who';
    who.textContent = 'allow this?';
    wrap.appendChild(who);
    var title = document.createElement('div');
    title.className = 'ask-title';
    title.textContent = (msg && msg.title) || 'run a command';
    wrap.appendChild(title);
    if (msg && msg.command && String(msg.command) && title.textContent.indexOf(msg.command) < 0) {
      var pre = document.createElement('pre');
      pre.className = 'ask-cmd';
      pre.textContent = msg.command;
      wrap.appendChild(pre);
    }
    var actions = document.createElement('div');
    var opts = (msg && msg.options) || [];
    if (opts.length) {
      actions.className = 'ask-opts';
      opts.forEach(function (o, i) {
        var id = o && (o.id || o.optionId);
        var name = (o && o.name) || id;
        if (!id || !name) return;
        var b = document.createElement('button');
        b.type = 'button';
        b.className = 'ask-opt';
        var lab = document.createElement('div');
        lab.className = 'ask-label';
        lab.textContent = (i + 1) + '. ' + name;
        b.appendChild(lab);
        b.addEventListener('click', function () { finish(id, name); });
        actions.appendChild(b);
      });
    } else {
      actions.className = 'ask-actions';
      var allow = document.createElement('button');
      allow.type = 'button';
      allow.className = 'ask-submit';
      allow.textContent = 'Allow';
      allow.addEventListener('click', function () { finish('allow', 'allowed'); });
      var deny = document.createElement('button');
      deny.type = 'button';
      deny.className = 'ask-skip';
      deny.textContent = 'Deny';
      deny.addEventListener('click', function () { finish('deny', 'denied'); });
      actions.appendChild(allow);
      actions.appendChild(deny);
    }
    wrap.appendChild(actions);
    s.el.appendChild(wrap);
    s.askEl = wrap;
    s.scroll();

    function finish(action, label) {
      if (!s.asking) return;
      // An Allow that never left the browser must not read as "allowed".
      if (!s.ws || s.ws.readyState !== 1) {
        s.setChrome('disconnected — not sent', 'err');
        return;
      }
      s.asking = false;
      wrap.classList.add('done');
      var buttons = wrap.querySelectorAll('button');
      for (var i = 0; i < buttons.length; i++) buttons[i].disabled = true;
      s.ws.send(JSON.stringify({ type: 'perm', action: action }));
      var note = document.createElement('div');
      note.className = 'ask-picked';
      var act = String(action || '');
      if (label && label !== action) note.textContent = label;
      else if (act === 'deny' || /reject/.test(act)) note.textContent = 'denied';
      else if (/always/.test(act)) note.textContent = 'always-approve';
      else note.textContent = 'allowed';
      wrap.appendChild(note);
      s.setChrome('working…', 'busy');
    }
  };

  Session.prototype.addErr = function (text) {
    var wrap = document.createElement('div');
    wrap.className = 'msg err';
    wrap.textContent = text || 'error';
    this.el.appendChild(wrap);
    this.scroll();
  };

  // Close out a card that can no longer be answered. The server forgets a
  // pending request when its socket goes, so a reconnected tab must not
  // leave buttons that would report "allowed" while sending nothing.
  Session.prototype.retireAsk = function (note) {
    if (!this.asking || !this.askEl) return;
    this.asking = false;
    this.askKey = '';
    this.askEl.classList.add('done');
    var buttons = this.askEl.querySelectorAll('button');
    for (var i = 0; i < buttons.length; i++) buttons[i].disabled = true;
    var el = document.createElement('div');
    el.className = 'ask-picked';
    el.textContent = note || 'expired';
    this.askEl.appendChild(el);
  };

  // Shown once per session when the agent approves its own tool calls.
  Session.prototype.addWarn = function (text) {
    if (!text || this.warned) return;
    this.warned = true;
    var wrap = document.createElement('div');
    wrap.className = 'msg warn';
    wrap.textContent = text;
    this.el.appendChild(wrap);
    this.scroll();
  };

  Session.prototype.setChrome = function (text, cls) {
    this.statusText = text;
    this.statusCls = cls || '';
    if (this === active) setStatus(text, cls);
  };

  // How long a session keeps redialling through pre-ready errors before it
  // stops and waits to be used again. An agent restart is the thing being
  // ridden out, and it takes longer than a handful of dials.
  var handshakeGraceMs = 60000;

  // Whether a pre-ready error means the id we asked to resume is gone
  // rather than the agent being briefly away. The wording comes from grok,
  // so this looks for the two ideas together — a session, and it not being
  // there — and treats anything else as worth another dial. Guessing wrong
  // this way costs a retry; guessing wrong the other way loses the session.
  var gonePhrase = /not found|no such|unknown|invalid|missing|does not exist|expired|corrupt/i;
  var sessionWord = /session|conversation|thread/i;

  function unloadableSession(text, id) {
    var t = String(text || '');
    if (!gonePhrase.test(t)) return false;
    if (sessionWord.test(t)) return true;
    return !!id && t.toLowerCase().indexOf(String(id).toLowerCase()) >= 0;
  }

  Session.prototype.connect = function () {
    var s = this;
    // A retry timer armed before shutdown() must not resurrect the tab.
    if (s.dead) return;
    if (s.retry) {
      clearTimeout(s.retry);
      s.retry = 0;
    }
    var prev = s.ws;
    s.ws = null;
    if (prev) {
      try { prev.close(); } catch (e) {}
    }
    s.busy = true;
    s.live = false;
    if (s === active) setBusy(true);
    s.setChrome(s.reconnects ? 'reconnecting…' : 'connecting…');
    var replay = !!(s.resumeID && !s.seenReady);
    var ws = new WebSocket(paneWS(s.cwd, s.resumeID, replay));
    s.ws = ws;
    ws.onopen = function () { s.setChrome('handshaking…', 'busy'); };
    ws.onerror = function () { s.setChrome('socket error', 'err'); };
    ws.onclose = function () {
      if (s.dead || s.ws !== this) return;
      s.busy = true;
      s.live = false;
      if (s === active) setBusy(true);
      // The server already said why it could not open this session;
      // leave that on screen instead of redialling into the same error.
      if (s.giveUp) return;
      s.setChrome('disconnected', 'err');
      s.reconnects++;
      s.retry = setTimeout(function () { s.connect(); }, Math.min(8000, 400 * s.reconnects));
    };
    ws.onmessage = function (ev) {
      var msg;
      try { msg = JSON.parse(ev.data); } catch (e) { return; }
      if (foreignSession(s, msg)) return;
      switch (msg.type) {
        case 'ready':
          // A reconnect is a new server-side session: whatever it was
          // waiting on is gone, so an unanswered card cannot be answered.
          if (s.seenReady) s.retireAsk('expired — reconnected');
          s.reconnects = 0;
          s.handshakeSince = 0;
          s.seenReady = true;
          s.live = true;
          s.id = msg.session || s.id;
          if (s.id) s.resumeID = s.id;
          if (msg.cwd) s.cwd = msg.cwd;
          if (s === active) paintCwd(s.cwd);
          // Reattaching to a session that is still answering has to show
          // Queue, not a Send that would race the turn. `busy` is the
          // server's word for that; a server too old to send it leaves
          // this undefined, which reads as idle exactly as it used to.
          var midTurn = msg.busy === true;
          s.busy = midTurn;
          s.setChrome(midTurn ? 'working…' : 'ready', midTurn ? 'busy' : 'ok');
          if (s === active) setBusy(midTurn);
          else paintSessions();
          applyCatalog(s, msg);
          if (s === active) paintCatalog();
          if (s.id && s.cwd) store('pane-last-sid:' + normPath(s.cwd), s.id);
          // The server picks its own cwd when the query had none (?sid=,
          // or a boot with no saved project). Without adopting it the
          // composer has a live session and still refuses to send.
          if (s.cwd && !project) setProject(s.cwd);
          refreshUsage(s);
          scheduleHistory(s.cwd);
          flushQueue(s);
          break;
        case 'you':
          s.addYou(msg.text || '', msg.files || []);
          break;
        case 'out':
          if (!s.startedReply) s.startedReply = true;
          s.addOut(msg.text || '');
          break;
        case 'thought':
          s.addThought(msg.text || '');
          break;
        case 'ask':
          s.addAsk(msg);
          s.setChrome('waiting for you…', 'busy');
          if (s === active) setBusy(true);
          break;
        case 'perm':
          s.addPerm(msg);
          s.setChrome('waiting for you…', 'busy');
          if (s === active) setBusy(true);
          break;
        case 'tool':
          renderTool(s, msg);
          break;
        case 'err':
          s.addErr(msg.text || 'error');
          s.setChrome(msg.text || 'error', 'err');
          // An error before ready means the handshake itself failed and
          // the socket is about to close. Only an error that says this
          // session cannot be loaded is a reason to stop asking for it —
          // "no grok agent at …", "agent closed" and a failed dial all
          // mean the agent is between lives, and dropping the id there
          // would lose the session the user was sitting in.
          var transient = /agent closed|no grok agent|not reachable|connection refused|socket error/i.test(msg.text || '');
          if (s.seenReady && transient) {
            s.giveUp = false;
            if (s.ws && s.ws.readyState === 1) {
              try { s.ws.close(); } catch (e) {}
            }
          }
          if (!s.seenReady) {
            if (s.resumeID && unloadableSession(msg.text, s.resumeID)) {
              forget('pane-last-sid:' + normPath(s.cwd));
              s.resumeID = '';
              s.handshakeSince = 0;
            } else if (transient) {
              s.handshakeSince = 0;
            } else {
              if (!s.handshakeSince) s.handshakeSince = Date.now();
              if (Date.now() - s.handshakeSince >= handshakeGraceMs) {
                // The redial backs off to eight seconds, so counting
                // attempts is a poor clock: this is a whole minute of the
                // pane being unreachable, which outlasts an agent
                // restart. Past it a red line every few seconds tells the
                // user nothing new, and sending dials again.
                s.giveUp = true;
                s.setChrome((msg.text || 'error') + ' — send to retry', 'err');
              }
            }
          }
          break;
        case 'warn':
          s.addWarn(msg.text || '');
          break;
        case 'busy':
          s.busy = true;
          s.asking = false;
          s.askKey = '';
          s.startedReply = false;
          s.tools = {};
          s.toolsBox = null;
          s.toolsList = null;
          s.toolsSum = null;
          s.toolCount = 0;
          s.agentBuf = '';
          s.agentEl = null;
          s.thoughtBuf = '';
          s.thoughtEl = null;
          s.setChrome('working…', 'busy');
          if (s === active) setBusy(true);
          break;
        case 'idle':
          s.busy = false;
          s.asking = false;
          s.setChrome('ready', 'ok');
          if (s === active) setBusy(false);
          else paintSessions();
          refreshUsage(s);
          flushQueue(s);
          break;
        case 'usage':
          s.used = +msg.used || 0;
          if (msg.size) s.context = +msg.size;
          if (s === active) paintUsage();
          break;
        case 'model':
          applyCatalog(s, msg);
          if (s === active) paintCatalog();
          break;
        case 'effort':
          s.effort = msg.id || s.effort;
          if (s === active) paintCatalog();
          break;
      }
    };
  };

  function liveToolTitle(s) {
    var tools = s && s.tools;
    if (!tools) return '';
    var keys = Object.keys(tools);
    var i;
    for (i = keys.length - 1; i >= 0; i--) {
      var t = tools[keys[i]];
      if (!t) continue;
      if (t.status === 'completed' || t.status === 'failed' || t.status === 'cancelled') continue;
      return t.title || '';
    }
    return '';
  }

  function renderTool(s, msg) {
    var id = msg.id || msg.text || 'tool';
    var prev = s.tools[id];
    var st = msg.status || '';
    var title = msg.text || 'tool';
    var ask = /ask_user|ask user|^ask:/i.test(title);
    if (!prev) {
      s.tools[id] = { title: title, status: st };
      if (!ask) s.addTool(title);
    } else {
      if (title && title !== 'tool') prev.title = title;
      if (st && st !== prev.status) prev.status = st;
    }
    if (s.busy && !s.asking && !ask) {
      s.setChrome(liveToolTitle(s) || title || 'working…', 'busy');
    }
  }

  function resumeOpts(h) {
    h = h || {};
    return {
      sid: h.id,
      title: h.title || h.id,
      talked: !!(h.messages > 0 || h.lastTurn)
    };
  }

  function blankSession(cwd) {
    cwd = cwd || project;
    var i;
    for (i = 0; i < sessions.length; i++) {
      var s = sessions[i];
      if (s.dead || s.talked) continue;
      if (cwd && s.cwd && !samePath(s.cwd, cwd)) continue;
      return s;
    }
    return null;
  }

  function syncNewSession() {
    var ok = !blankSession(project);
    if (!newBtn) return;
    newBtn.disabled = !ok;
    newBtn.setAttribute('aria-disabled', ok ? 'false' : 'true');
    newBtn.title = ok ? 'Start a new session' : 'Send a message first';
  }

  function newSession(cwd, opts) {
    opts = opts || {};
    if (opts.sid) {
      var existing = sessions.filter(function (x) { return x.id === opts.sid || x.resumeID === opts.sid; })[0];
      if (existing) {
        activate(existing);
        return existing;
      }
    } else {
      var blank = blankSession(cwd || project);
      if (blank) {
        activate(blank);
        syncNewSession();
        return blank;
      }
    }
    var s = new Session(cwd || project);
    if (opts.sid) {
      s.resumeID = opts.sid;
      if (opts.talked) s.talked = true;
    }
    if (opts.title) {
      s.title = opts.title;
      s.named = !looksLikeSessionID(opts.title);
    }
    sessions.push(s);
    sessPaintKey = '';
    paintSessions();
    s.connect();
    activate(s);
    syncNewSession();
    return s;
  }

  function loadHistory(cwd, opts) {
    opts = opts || {};
    if (!cwd) {
      return;
    }
    var keep = [];
    sessions.forEach(function (s) {
      if (s.id) keep.push(s.id);
      if (s.resumeID && keep.indexOf(s.resumeID) < 0) keep.push(s.resumeID);
    });
    var url = paneHTTP() + '/v1/sessions?cwd=' + encodeURIComponent(cwd);
    if (opts.prune) {
      url += '&prune=1';
      if (keep.length) url += '&keep=' + keep.map(encodeURIComponent).join(',');
    }
    // Pruning deletes sessions, so it goes out as a POST.
    fetchJSON(url, null, opts.prune ? { method: 'POST' } : null)
      .then(function (list) {
        if (project && !samePath(cwd, project)) return;
        diskSessions = Array.isArray(list) ? list : [];
        sessPaintKey = '';
        applyGrokTitles(diskSessions);
        paintSessions();
        if (opts.resume) {
          resumeLatest(cwd, diskSessions, diskSessions.length < sessionListCap);
          if (!liveSessionFor(cwd)) newSession(cwd);
        }
        loadProjects();
      })
      .catch(function () {
        if (project && !samePath(cwd, project)) return;
        diskSessions = [];
        sessPaintKey = '';
        paintSessions();
        if (opts.resume) resumeLatest(cwd, [], false);
        var opened = sessions.some(function (s) { return !s.dead && samePath(s.cwd, cwd); });
        if (!opened) {
          if (opts.resume) newSession(cwd);
          else setStatus('pane not reachable', 'err');
        }
        loadProjects();
      });
  }

  function applyGrokTitles(list) {
    var changed = false;
    (list || []).forEach(function (h) {
      if (!h || !h.id || !h.title) return;
      sessions.forEach(function (s) {
        if (s.named) return;
        if (s.id !== h.id && s.resumeID !== h.id) return;
        if (s.title !== h.title) {
          s.title = h.title;
          changed = true;
        }
      });
    });
    if (changed) {
      sessPaintKey = '';
      paintSessions();
    }
  }

  function commitProjectRename(cwd, val) {
    val = String(val || '').replace(/\s+/g, ' ').trim();
    renamingProject = null;
    if (!cwd) {
      loadProjects();
      return;
    }
    authFetch(paneHTTP() + '/v1/projects?cwd=' + encodeURIComponent(cwd), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: val })
    })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (info) {
        if (info && info.name) projectNames[normPath(cwd)] = info.name;
        else if (val) projectNames[normPath(cwd)] = val;
        else delete projectNames[normPath(cwd)];
        if (samePath(cwd, project) && projectBtn) {
          projectBtn.textContent = projectLabel(project);
        }
        loadProjects();
      })
      .catch(function () { loadProjects(); });
  }

  function startProjectRename(cwd) {
    cwd = normPath(cwd);
    if (!cwd || renamingProject === cwd) return;
    renamingProject = cwd;
    loadProjects();
  }

  function paintProjectList(list) {
    list = Array.isArray(list) ? list.slice() : [];
    list.forEach(function (p) {
      if (p && p.cwd && p.name) projectNames[normPath(p.cwd)] = p.name;
    });
    if (project) {
      var seen = list.some(function (p) { return p && samePath(p.cwd, project); });
      if (!seen) {
        list = [{ cwd: project, name: projectLabel(project), sessions: 0 }].concat(list);
      }
    }
    projectListCache = list.map(function (p) { return p && p.cwd ? normPath(p.cwd) : ''; }).filter(Boolean);
    if (!projectsEl) return;
    if (renamingProject && projectsEl.querySelector('.sess-edit')) return;
    projectsEl.textContent = '';
    list.forEach(function (p) {
      if (!p || !p.cwd) return;
      var row = document.createElement('div');
      row.className = 'sess-row';
      if (renamingProject && samePath(p.cwd, renamingProject)) {
        var inp = document.createElement('input');
        inp.type = 'text';
        inp.className = 'sess-edit';
        inp.value = p.name || projectLabel(p.cwd);
        inp.setAttribute('aria-label', 'Project name');
        // The repaint waits on loadProjects()'s fetch, so a click landing
        // in that window would otherwise blur-commit the cancelled name.
        var cancelledProj = false;
        inp.addEventListener('keydown', function (e) {
          if (e.key === 'Enter') {
            e.preventDefault();
            commitProjectRename(p.cwd, inp.value);
          }
          if (e.key === 'Escape') {
            e.preventDefault();
            cancelledProj = true;
            renamingProject = null;
            loadProjects();
          }
          e.stopPropagation();
        });
        inp.addEventListener('blur', function () {
          if (cancelledProj) return;
          commitProjectRename(p.cwd, inp.value);
        });
        row.appendChild(inp);
        projectsEl.appendChild(row);
        setTimeout(function () {
          inp.focus();
          inp.select();
        }, 0);
        return;
      }
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'hist' + (samePath(p.cwd, project) ? ' active' : '');
      b.textContent = p.name || projectLabel(p.cwd);
      b.title = p.cwd + (p.sessions ? '\n' + p.sessions + ' Grok sessions' : '') + '\ndouble-click to rename';
      b.addEventListener('click', function () {
        if (samePath(p.cwd, project)) {
          startProjectRename(p.cwd);
          return;
        }
        setProject(p.cwd);
      });
      b.addEventListener('dblclick', function (ev) {
        ev.preventDefault();
        ev.stopPropagation();
        startProjectRename(p.cwd);
      });
      var x = document.createElement('button');
      x.type = 'button';
      x.className = 'sess-close';
      x.title = 'Delete this Grok project (all its sessions)';
      x.textContent = '×';
      x.addEventListener('click', function (ev) {
        ev.stopPropagation();
        wipeProject(p);
      });
      row.appendChild(b);
      row.appendChild(x);
      projectsEl.appendChild(row);
    });
    if (!projectsEl.childNodes.length) {
      var empty = document.createElement('div');
      empty.className = 'hist';
      empty.style.cursor = 'default';
      empty.textContent = 'no grok projects';
      projectsEl.appendChild(empty);
    }
    if (project && projectBtn) projectBtn.textContent = projectLabel(project);
  }

  function loadProjects() {
    if (!projectsEl) return;
    fetchJSON(paneHTTP() + '/v1/projects')
      .then(paintProjectList)
      .catch(function () { paintProjectList([]); });
  }

  var histSoon = {};
  function scheduleHistory(cwd) {
    if (!cwd) return;
    if (histSoon[cwd]) clearTimeout(histSoon[cwd]);
    histSoon[cwd] = setTimeout(function () {
      delete histSoon[cwd];
      loadHistory(cwd);
    }, 600);
  }

  // What /v1/sessions will return at most (history.go: listGrokSessions
  // with a limit of 40). A response that hits the cap is a page, not a
  // census: it is sorted by Updated, and a session grok has not written a
  // summary for yet sorts last on an empty Updated, so the very session
  // being resumed is the one a full page drops.
  var sessionListCap = 40;

  function resumeLatest(cwd, list, listComplete) {
    list = list || [];
    var lastSid = stored('pane-last-sid:' + normPath(cwd));
    var pick = null;
    var i;
    if (lastSid) {
      for (i = 0; i < list.length; i++) {
        if (list[i] && list[i].id === lastSid) {
          pick = list[i];
          break;
        }
      }
    }
    // Only a list that is known complete can prove the stored id is gone:
    // then it was deleted or pruned, and resuming it yields an error and a
    // redial. A list that never arrived, or one long enough to have been
    // truncated, proves nothing — keep the id and let the handshake say.
    if (!pick && lastSid && !listComplete) pick = { id: lastSid, cwd: cwd, title: lastSid };
    if (!pick && lastSid && listComplete) forget('pane-last-sid:' + normPath(cwd));
    if (!pick && list[0] && list[0].id) pick = list[0];
    if (pick && pick.id) {
      var existing = sessions.filter(function (s) {
        return !s.dead && (s.id === pick.id || s.resumeID === pick.id);
      })[0];
      if (existing) {
        activate(existing);
        return;
      }
      newSession(cwd, resumeOpts(pick));
      return;
    }
    var open = sessions.filter(function (s) { return !s.dead && samePath(s.cwd, cwd); })[0];
    if (open) activate(open);
    else if (!active || active.dead) setStatus('', 'ok');
  }

  function loadRemote() {
    if (!remoteEl) return;
    authFetch(paneHTTP() + '/v1/remote-sessions')
      .then(function (r) { return r.ok ? r.json() : []; })
      .then(function (list) {
        remoteEl.textContent = '';
        (list || []).forEach(function (h) {
          if (!h || !h.id) return;
          var row = document.createElement('div');
          row.className = 'sess-row';
          var b = document.createElement('button');
          b.type = 'button';
          b.className = 'hist';
          var title = h.title || h.id;
          b.textContent = title;
          var sub = document.createElement('span');
          sub.className = 'hist-sub';
          sub.textContent = (h.host || '') + (h.cwd ? ' · ' + h.cwd : '');
          b.appendChild(document.createElement('br'));
          b.appendChild(sub);
          b.title = (h.origin || '') + '\n' + (h.cwd || '') + '\n' + (h.id || '');
          b.addEventListener('click', function () { openRemote(h); });
          row.appendChild(b);
          remoteEl.appendChild(row);
        });
        if (!remoteEl.childNodes.length) {
          var empty = document.createElement('div');
          empty.className = 'hist';
          empty.style.cursor = 'default';
          empty.textContent = 'no other tailnet panes';
          remoteEl.appendChild(empty);
        }
      })
      .catch(function () {
        if (!remoteEl.childNodes.length) {
          var empty = document.createElement('div');
          empty.className = 'hist';
          empty.style.cursor = 'default';
          empty.textContent = 'no other tailnet panes';
          remoteEl.appendChild(empty);
        }
      });
  }

  function sameOrigin(a, b) {
    try {
      var ua = new URL(a);
      var ub = new URL(b);
      return ua.origin === ub.origin;
    } catch (e) {
      return a === b;
    }
  }

  function openRemote(h) {
    if (!h || !h.id) return;
    var origin = (h.origin || '').replace(/\/$/, '');
    if (!origin || sameOrigin(origin, paneHTTP())) {
      if (h.cwd) setProject(h.cwd);
      newSession(h.cwd || project, resumeOpts(h));
      return;
    }
    try {
      localStorage.setItem('pane-pending-session', JSON.stringify({
        cwd: h.cwd || '',
        sid: h.id,
        title: h.title || ''
      }));
    } catch (e) {}
    var api = desktopAPI();
    if (api && typeof api.SetPaneOrigin === 'function') {
      Promise.resolve(api.SetPaneOrigin(origin)).then(function (got) {
        if (String(got).indexOf('error:') === 0) {
          setStatus(got, 'err');
          return;
        }
        try { localStorage.setItem('pane-url', got); } catch (e) {}
        window.__paneOrigin = got;
        location.reload();
      }).catch(function () { setStatus('could not reach ' + origin, 'err'); });
      return;
    }
    location.href = origin + '/?cwd=' + encodeURIComponent(h.cwd || '') + '&sid=' + encodeURIComponent(h.id) + '&title=' + encodeURIComponent(h.title || '');
  }

  var modalEl = document.getElementById('modal');
  var modalText = document.getElementById('modal-text');
  var modalOk = document.getElementById('modal-ok');
  var modalCancel = document.getElementById('modal-cancel');
  var modalDone = null;

  function closeModal(ok) {
    if (!modalEl) return;
    modalEl.hidden = true;
    var fn = modalDone;
    modalDone = null;
    if (fn) fn(!!ok);
  }

  function askConfirm(message, fn) {
    if (!modalEl || !modalText) {
      fn(false);
      return;
    }
    if (modalDone) closeModal(false);
    modalText.textContent = message;
    modalEl.hidden = false;
    modalDone = fn;
    if (modalOk) modalOk.focus();
  }

  if (modalOk) modalOk.addEventListener('click', function () { closeModal(true); });
  if (modalCancel) modalCancel.addEventListener('click', function () { closeModal(false); });
  if (modalEl) {
    modalEl.addEventListener('click', function (e) {
      if (e.target === modalEl) closeModal(false);
    });
  }
  window.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && modalEl && !modalEl.hidden) {
      e.preventDefault();
      e.stopPropagation();
      closeModal(false);
    }
  }, true);

  function dropTab(s) {
    if (!s) return;
    var i = sessions.indexOf(s);
    if (i >= 0) sessions.splice(i, 1);
    // shutdown() hands back the composer draft of the tab that is on
    // screen, so it has to still be the active one while it runs.
    s.shutdown();
    if (s === active) active = null;
    syncNewSession();
  }

  function wipeProject(p) {
    if (!p || !p.cwd) return;
    var name = p.name || basename(p.cwd);
    var n = p.sessions || 0;
    askConfirm('Delete project “' + name + '” permanently?\nThis removes all ' + n + ' Grok session' + (n === 1 ? '' : 's') + ' for\n' + p.cwd + '\nThe project files on disk stay.', function (ok) {
      if (!ok) return;
      var doomed = [];
      sessions.forEach(function (s) {
        if (s.cwd === p.cwd) doomed.push(s);
      });
      doomed.forEach(dropTab);
      authFetch(paneHTTP() + '/v1/projects?cwd=' + encodeURIComponent(p.cwd), { method: 'DELETE' })
        .then(function (r) {
          if (!r.ok) throw new Error('delete failed');
          setStatus('deleted project', 'ok');
          if (project === p.cwd) {
            project = '';
            if (projectBtn) {
              projectBtn.textContent = 'Open project';
              projectBtn.title = 'Open a project folder';
            }
            try { localStorage.removeItem('pane-project'); } catch (e) {}
            diskSessions = [];
            sessPaintKey = '';
            paintCwd('');
            loadProjects();
            if (!sessions.length) {
              authFetch(paneHTTP() + '/v1/projects')
                .then(function (r) { return r.ok ? r.json() : []; })
                .then(function (list) {
                  if (list && list[0] && list[0].cwd) setProject(list[0].cwd);
                  else paintSessions();
                })
                .catch(function () { paintSessions(); });
              return;
            }
            if (!active) activate(sessions[0]);
            paintSessions();
            return;
          }
          loadProjects();
          paintSessions();
        })
        .catch(function () {
          setStatus('could not delete project', 'err');
          loadProjects();
        });
    });
  }

  function wipeSession(cwd, id, title, tab) {
    cwd = cwd || project;
    id = id || (tab && (tab.id || tab.resumeID)) || '';
    var label = title || id || 'this session';
    if (!cwd) return;
    askConfirm('Delete “' + label + '” permanently?\nThis removes it from Grok’s history on disk.', function (ok) {
      if (!ok) return;
      var doomed = [];
      sessions.forEach(function (s) {
        if (s === tab || (id && (s.id === id || s.resumeID === id))) doomed.push(s);
      });
      doomed.forEach(dropTab);
      if (!sessions.length) newSession(project || cwd);
      else if (!active) activate(sessions[0]);
      paintSessions();
      if (!id) {
        loadHistory(cwd);
        return;
      }
      authFetch(paneHTTP() + '/v1/sessions?cwd=' + encodeURIComponent(cwd) + '&id=' + encodeURIComponent(id), { method: 'DELETE' })
        .then(function (r) {
          if (!r.ok) throw new Error('delete failed');
          setStatus('deleted', 'ok');
          loadHistory(cwd);
        })
        .catch(function () {
          setStatus('could not delete session', 'err');
          loadHistory(cwd);
        });
    });
  }

  Session.prototype.shutdown = function () {
    this.dead = true;
    this.live = false;
    // A pending reconnect outlives the tab otherwise: it would dial a new
    // socket for a session the user just deleted, re-store its id, and
    // flush the queue into it.
    if (this.retry) {
      clearTimeout(this.retry);
      this.retry = 0;
    }
    var w = this.ws;
    this.ws = null;
    if (w) {
      try { w.close(); } catch (e) {}
    }
    // This tab held the only reference to whatever its queued messages
    // uploaded, so the copies in the project go with it.
    var cwd = this.cwd;
    (this.queue || []).forEach(function (item) {
      releaseFiles(item && item.files, cwd);
    });
    this.queue = [];
    // The composer is shared, but the draft in it belongs to the tab on
    // screen and its attachments were already copied into that tab's
    // project. Nothing else remembers them once the tab is gone.
    if (this === active && pending.length) {
      releaseFiles(pending, cwd);
      pending = [];
      paintChips();
      syncSend();
    }
    if (this.el && this.el.parentNode) this.el.parentNode.removeChild(this.el);
  };

  function closeSession(s) {
    s = s || active;
    if (!s) return;
    if (turnInFlight(s)) {
      setStatus('finish the current turn first', 'err');
      return;
    }
    var i = sessions.indexOf(s);
    if (i < 0) return;
    sessions.splice(i, 1);
    s.shutdown();
    if (s === active) active = null;
    if (sessions.length) activate(sessions[Math.min(i, sessions.length - 1)]);
    else newSession(project);
    paintSessions();
    syncNewSession();
    if (s.cwd) loadHistory(s.cwd);
  }

  function applyCatalog(s, msg) {
    if (!s || !msg) return;
    if (msg.models && msg.models.length) s.models = msg.models;
    if (msg.model) s.model = msg.model;
    if (msg.id && msg.type === 'model') s.model = msg.id;
    if (msg.effort) s.effort = msg.effort;
    if (msg.context) s.context = +msg.context;
  }

  var catalogKey = '';
  function paintCatalog() {
    if (!modelEl || !effortEl) return;
    var s = active;
    var models = (s && s.models) ? s.models : [];
    var cur = s && s.model ? s.model : stored('pane-model');
    var eff = s && s.effort ? s.effort : stored('pane-effort');
    var key = cur + '\t' + eff + '\t' + models.map(function (m) { return m.id; }).join(',');
    if (key === catalogKey && modelEl.options.length) return;
    catalogKey = key;
    modelEl.textContent = '';
    if (!models.length) {
      var o = document.createElement('option');
      o.value = cur || '';
      o.textContent = cur || 'Model';
      modelEl.appendChild(o);
    } else {
      models.forEach(function (m) {
        var o = document.createElement('option');
        o.value = m.id;
        o.textContent = m.name || m.id;
        if (m.id === cur) o.selected = true;
        modelEl.appendChild(o);
      });
    }
    var efforts = [];
    models.forEach(function (m) {
      if (m.id === (modelEl.value || cur) && m.efforts) efforts = m.efforts;
    });
    if (!efforts.length) {
      efforts = [
        { id: 'xhigh', label: 'Extra High' },
        { id: 'high', label: 'High' },
        { id: 'medium', label: 'Medium' },
        { id: 'low', label: 'Low' }
      ];
    }
    effortEl.textContent = '';
    efforts.forEach(function (e) {
      var o = document.createElement('option');
      o.value = e.id;
      o.textContent = shortEffort(e.label || e.id);
      if (e.id === eff) o.selected = true;
      effortEl.appendChild(o);
    });
    if (eff && effortEl.value !== eff) {
      var extra = document.createElement('option');
      extra.value = eff;
      extra.textContent = shortEffort(eff);
      extra.selected = true;
      effortEl.appendChild(extra);
    }
  }

  function shortEffort(label) {
    var t = String(label || '');
    if (/extra|xhigh/i.test(t)) return 'XHigh';
    if (/high/i.test(t)) return 'High';
    if (/medium/i.test(t)) return 'Med';
    if (/low/i.test(t)) return 'Low';
    return t;
  }

  function paintUsage() {
    if (!usageRing || !usageEl) return;
    var used = active ? (active.used || 0) : 0;
    var size = active ? (active.context || 0) : 0;
    var pct = size > 0 ? Math.max(0, Math.min(1, used / size)) : 0;
    var circ = 2 * Math.PI * 7;
    usageRing.style.strokeDasharray = String(circ);
    usageRing.style.strokeDashoffset = String(circ * (1 - pct));
    usageEl.classList.toggle('warn', pct >= 0.7 && pct < 0.9);
    usageEl.classList.toggle('hot', pct >= 0.9);
    var title = size ? (Math.round(pct * 100) + '% · ' + fmtNum(used) + ' / ' + fmtNum(size) + ' — click for details') : 'Context usage — click for details';
    usageEl.title = title;
    usageEl.setAttribute('aria-label', title);
    if (usagePop && !usagePop.hidden) fillUsagePop();
  }

  function fmtUSD(n) {
    n = Number(n) || 0;
    if (!n) return '$0.00';
    if (Math.round(n) === n) return '$' + n;
    return '$' + n.toFixed(2);
  }

  function fmtReset(s) {
    var d = new Date(s);
    if (!isNaN(d.getTime())) {
      return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
    }
    return String(s || '').replace('T', ' ').replace(/\+00:00$/, ' UTC').replace(/\.\d+Z$/, 'Z');
  }

  function productUsageLabel(name) {
    var map = { GrokBuild: 'Grok Build', GrokChat: 'Grok Chat', GrokImagine: 'Grok Imagine' };
    return map[name] || String(name || 'Grok');
  }

  function fmtDur(sec) {
    sec = Math.round(sec || 0);
    if (sec < 60) return sec + 's';
    if (sec < 3600) return Math.floor(sec / 60) + 'm ' + (sec % 60) + 's';
    return Math.floor(sec / 3600) + 'h ' + Math.floor((sec % 3600) / 60) + 'm';
  }

  function fillUsagePop() {
    if (!usagePop) return;
    var u = (active && active.usage) || {};
    var used = (active && active.used) || u.used || 0;
    var size = (active && active.context) || u.size || 0;
    var pct = size > 0 ? Math.round(used * 100 / size) : (u.pct || 0);
    var left = (u.left != null && u.left >= 0) ? u.left : (size > used ? size - used : 0);
    var model = (active && active.model) || u.model || '—';
    var effort = (active && active.effort) || '—';
    var tools = (u.tools && u.tools.length) ? u.tools.join(', ') : '—';
    var turns = u.turns != null ? u.turns : '—';
    var calls = u.toolCalls != null ? u.toolCalls : '—';
    var dur = u.duration ? fmtDur(u.duration) : '—';
    usagePop.innerHTML = '';
    function heading(text) {
      var h = document.createElement('h3');
      h.textContent = text;
      usagePop.appendChild(h);
    }
    function para(cls, text) {
      var p = document.createElement('p');
      p.className = cls;
      p.textContent = text;
      usagePop.appendChild(p);
    }
    var dl = document.createElement('dl');
    function row(k, v) {
      var dt = document.createElement('dt');
      dt.textContent = k;
      var dd = document.createElement('dd');
      dd.textContent = v;
      dl.appendChild(dt);
      dl.appendChild(dd);
    }
    heading('Context');
    para('u-big', size ? (fmtNum(used) + ' / ' + fmtNum(size) + ' tokens') : 'No usage yet');
    para('u-sub', size ? (pct + '% used · ' + fmtNum(left) + ' left') : 'Send a message to start counting.');
    if (u.compactAt) {
      para('u-sub', 'Auto-compact around ' + fmtNum(u.compactAt) + ' (80%)');
    }
    heading('Usage limits');
    if (u.limitKind === 'credits' || u.limitWeekly) {
      var weekPct = u.limitPct != null ? u.limitPct : 0;
      para('u-big', weekPct + '% of weekly included');
      para('u-sub', weekPct >= 100
        ? 'Weekly included credits used up'
        : ((100 - weekPct) + '% remaining this week'));
      if (u.limitProducts && u.limitProducts.length) {
        para('u-sub', u.limitProducts.map(function (p) {
          return productUsageLabel(p.product) + ' ' + p.pct + '%';
        }).join(' · '));
      }
      if (u.limitReset) para('u-sub', 'Resets ' + fmtReset(u.limitReset));
      if (u.limitPrepaid != null) para('u-sub', 'Extra credits ' + fmtUSD(u.limitPrepaid));
      if (u.limitOnDemand) row('On-demand cap', fmtUSD(u.limitOnDemand));
      if (u.limitNote && weekPct < 100) para('u-sub', u.limitNote);
    } else if (u.limitMonthly) {
      para('u-big', fmtUSD(u.limitUsed) + ' / ' + fmtUSD(u.limitMonthly) + ' this month');
      var remain = u.limitMonthly - u.limitUsed;
      para('u-sub', remain >= 0
        ? (fmtUSD(remain) + ' included remaining')
        : (fmtUSD(-remain) + ' over included limit'));
      if (u.limitReset) para('u-sub', 'Resets ' + fmtReset(u.limitReset));
      if (u.limitOnDemand) row('On-demand cap', fmtUSD(u.limitOnDemand));
      if (u.limitNote) para('u-sub', u.limitNote);
    } else {
      para('u-sub', 'Account limits load from grok when you are signed in.');
    }
    var manage = document.createElement('button');
    manage.type = 'button';
    manage.className = 'u-link';
    manage.textContent = 'Manage on grok.com';
    manage.addEventListener('click', function (e) {
      e.stopPropagation();
      openExternal('https://grok.com/?_s=usage');
    });
    usagePop.appendChild(manage);
    heading('Session');
    row('Model', model);
    row('Effort', shortEffort(effort));
    row('Turns', String(turns));
    row('Tools', String(calls) + (tools !== '—' ? ' · ' + tools : ''));
    row('Duration', dur);
    usagePop.appendChild(dl);
  }

  function setUsageOpen(on) {
    if (!usagePop || !usageEl) return;
    usagePop.hidden = !on;
    usageEl.setAttribute('aria-expanded', on ? 'true' : 'false');
    if (on) {
      fillUsagePop();
      if (active) refreshUsage(active);
    }
  }

  if (usageEl) {
    usageEl.addEventListener('click', function (e) {
      e.stopPropagation();
      setUsageOpen(usagePop && usagePop.hidden);
    });
  }
  if (usageWrap) {
    usageWrap.addEventListener('click', function (e) { e.stopPropagation(); });
  }
  document.addEventListener('click', function () { setUsageOpen(false); });
  window.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && usagePop && !usagePop.hidden) {
      setUsageOpen(false);
    }
  });

  function fmtNum(n) {
    n = Math.round(n || 0);
    if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
    if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + 'k';
    return String(n);
  }

  function refreshUsage(s) {
    if (!s || !s.id || !s.cwd) {
      if (s === active) paintUsage();
      return;
    }
    authFetch(paneHTTP() + '/v1/usage?cwd=' + encodeURIComponent(s.cwd) + '&id=' + encodeURIComponent(s.id))
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (u) {
        if (!u) return;
        s.usage = u;
        if (u.used) s.used = +u.used;
        if (u.size) s.context = +u.size;
        if (s === active) paintUsage();
      })
      .catch(function () {});
  }

  if (modelEl) {
    modelEl.addEventListener('change', function () {
      var id = modelEl.value;
      store('pane-model', id);
      if (active) active.model = id;
      if (active && active.ws && active.ws.readyState === 1) {
        active.ws.send(JSON.stringify({ type: 'model', id: id }));
      }
      paintCatalog();
    });
  }
  if (effortEl) {
    effortEl.addEventListener('change', function () {
      var id = effortEl.value;
      store('pane-effort', id);
      if (active) active.effort = id;
      if (active && active.ws && active.ws.readyState === 1) {
        active.ws.send(JSON.stringify({ type: 'effort', id: id }));
      }
    });
  }

  function paintQueue() {
    if (!queueEl) return;
    queueEl.textContent = '';
    var q = (active && active.queue) ? active.queue : [];
    if (!q.length) {
      queueEl.hidden = true;
      return;
    }
    queueEl.hidden = false;
    q.forEach(function (item, i) {
      var text = queueText(item);
      var row = document.createElement('div');
      row.className = 'q-row';
      var mark = document.createElement('span');
      mark.className = 'q-mark';
      mark.textContent = 'queued';
      var t = document.createElement('button');
      t.type = 'button';
      t.className = 'q-text';
      t.textContent = text;
      t.title = 'Edit — put back in the composer';
      t.addEventListener('click', function () {
        if (!active) return;
        var cur = snapshotComposer();
        active.queue.splice(i, 1);
        if (cur.text || (cur.files && cur.files.length)) active.queue.splice(i, 0, cur);
        restoreComposer(item);
        grow();
        paintQueue();
        paintSessions();
        syncSend();
        input.focus();
      });
      var x = document.createElement('button');
      x.type = 'button';
      x.className = 'q-x';
      x.title = 'Remove from queue';
      x.textContent = '×';
      x.addEventListener('click', function () {
        if (!active) return;
        var gone = active.queue.splice(i, 1)[0];
        releaseFiles(gone && gone.files, active.cwd);
        paintQueue();
        paintSessions();
      });
      row.appendChild(mark);
      row.appendChild(t);
      row.appendChild(x);
      queueEl.appendChild(row);
    });
  }

  function queueText(item) {
    if (typeof item === 'string') return item;
    var t = (item && item.text) || '';
    var files = (item && item.files) || [];
    if (t && files.length) return t + ' · ' + files.map(function (f) { return f.name || 'file'; }).join(', ');
    if (t) return t;
    if (files.length) return files.map(function (f) { return f.name || 'file'; }).join(', ');
    return '(empty)';
  }

  function snapshotComposer() {
    var text = input.value.replace(/\s+$/, '');
    var files = pending.slice();
    input.value = '';
    pending = [];
    paintChips();
    grow();
    return { text: text, files: files };
  }

  function restoreComposer(item) {
    if (typeof item === 'string') {
      input.value = item;
      pending = [];
    } else {
      input.value = (item && item.text) || '';
      pending = ((item && item.files) || []).slice();
    }
    paintChips();
    grow();
  }

  function dispatch(s, item) {
    if (!s || s.dead || !s.ws || s.ws.readyState !== 1) return false;
    if (typeof item === 'string') item = { text: item, files: [] };
    var text = (item && item.text) || '';
    var files = (item && item.files) || [];
    if (!text && !files.length) return false;
    var label = text || queueText(item);
    if (!s.named && s.title.indexOf('Session ') === 0) {
      s.title = label.length > 28 ? label.slice(0, 28) + '…' : label;
    }
    s.addYou(text, files);
    s.startedReply = false;
    s.agentBuf = '';
    s.agentEl = null;
    s.thoughtBuf = '';
    s.thoughtEl = null;
    s.busy = true;
    if (s === active) setBusy(true);
    else paintSessions();
    s.setChrome('working…', 'busy');
    s.ws.send(JSON.stringify({
      type: 'in',
      text: text,
      files: files.map(function (f) {
        return { path: f.path, name: f.name, mime: f.mime, size: f.size };
      })
    }));
    return true;
  }

  function flushQueue(s) {
    if (!s || s.dead || s.busy || !s.ws || s.ws.readyState !== 1) return;
    if (!s.queue.length) {
      if (s === active) paintQueue();
      return;
    }
    var item = s.queue.shift();
    if (s === active) paintQueue();
    dispatch(s, item);
  }

  function composerHistKey() {
    return 'pane-composer:' + normPath(project || '');
  }

  function composerHistList() {
    try {
      var raw = localStorage.getItem(composerHistKey());
      var list = raw ? JSON.parse(raw) : [];
      return Array.isArray(list) ? list.filter(function (x) { return typeof x === 'string' && x; }) : [];
    } catch (e) { return []; }
  }

  function rememberComposer(text) {
    text = String(text || '').replace(/\s+$/, '');
    if (!text || !project) return;
    var list = composerHistList();
    if (list.length && list[list.length - 1] === text) {
      composerHistIdx = -1;
      composerHold = '';
      return;
    }
    list.push(text);
    if (list.length > 200) list = list.slice(-200);
    try { localStorage.setItem(composerHistKey(), JSON.stringify(list)); } catch (e) {}
    composerHistIdx = -1;
    composerHold = '';
  }

  function composerCaret() {
    var v = input.value;
    var start = input.selectionStart;
    var end = input.selectionEnd;
    if (start !== end) return { collapsed: false };
    return {
      collapsed: true,
      firstLine: v.lastIndexOf('\n', start - 1) === -1,
      lastLine: v.indexOf('\n', start) === -1
    };
  }

  function stepComposerHistory(dir) {
    var list = composerHistList();
    if (!list.length) return false;
    var caret = composerCaret();
    if (!caret.collapsed) return false;
    var browsing = composerHistIdx >= 0;
    if (!browsing) {
      if (dir > 0) return false;
      if (!caret.firstLine) return false;
      composerHold = input.value;
      composerHistIdx = list.length;
    } else if (dir < 0 && !caret.firstLine) {
      return false;
    } else if (dir > 0 && !caret.lastLine) {
      return false;
    }
    var next = composerHistIdx + dir;
    if (next < 0) next = 0;
    if (next > list.length) next = list.length;
    composerHistIdx = next;
    var text = composerHistIdx === list.length ? composerHold : list[composerHistIdx];
    composerHistApplying = true;
    input.value = text;
    grow();
    syncSend();
    var pos = input.value.length;
    input.setSelectionRange(pos, pos);
    composerHistApplying = false;
    return true;
  }

  function send() {
    var has = !!(input.value.replace(/\s+$/, '') || pending.length);
    if (!has) return;
    // The live session's own cwd is what the message goes to; the global
    // project can be empty for a session the server gave a cwd to, and
    // refusing then leaves a ready tab that cannot send. Matches addFiles.
    var cwd = (active && !active.dead && active.cwd) || project;
    if (!cwd) {
      setStatus('open a project first', 'err');
      return;
    }
    if (!active || active.dead) {
      newSession(cwd);
    }
    if (!active || active.dead) {
      setStatus('no session', 'err');
      return;
    }
    // Trying to use a session that stopped dialling is the retry: the
    // message queues and flushes as soon as the handshake lands.
    if (active.giveUp) {
      active.giveUp = false;
      active.reconnects = 0;
      active.handshakeSince = 0;
      active.connect();
    }
    var item = snapshotComposer();
    rememberComposer(item.text);
    if (!item.text && !(item.files && item.files.length)) return;
    var ready = active.ws && active.ws.readyState === 1 && !active.busy;
    if (!ready) {
      if (active.queue.length >= 20) {
        setStatus('queue full (20)', 'err');
        restoreComposer(item);
        syncSend();
        return;
      }
      active.queue.push(item);
      paintQueue();
      paintSessions();
      syncSend();
      if (!active.ws || active.ws.readyState !== 1) {
        setStatus('connecting… queued', 'busy');
      }
      return;
    }
    dispatch(active, item);
  }

  sendBtn.addEventListener('click', send);
  input.addEventListener('input', function () {
    if (!composerHistApplying) {
      composerHistIdx = -1;
      composerHold = '';
    }
    grow();
    syncSend();
  });
  input.addEventListener('paste', function (e) {
    var dt = e.clipboardData;
    if (!dt) return;
    var files = [];
    if (dt.files && dt.files.length) {
      files = dt.files;
    } else if (dt.items) {
      for (var i = 0; i < dt.items.length; i++) {
        if (dt.items[i].kind === 'file') {
          var f = dt.items[i].getAsFile();
          if (f) files.push(f);
        }
      }
    }
    if (!files.length) return;
    e.preventDefault();
    addFiles(files);
  });
  input.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
      return;
    }
    if (!e.metaKey && !e.ctrlKey && !e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
      if (stepComposerHistory(e.key === 'ArrowUp' ? -1 : 1)) {
        e.preventDefault();
        return;
      }
    }
    if (e.key !== 'Escape' || !active) return;
    // Only let Escape answer a card that can actually be answered;
    // otherwise fall through so it can still cancel the turn.
    if (active.asking && active.askEl && active.ws && active.ws.readyState === 1) {
      var skip = active.askEl.querySelector('.ask-skip');
      if (skip && !skip.disabled) skip.click();
      return;
    }
    if (active.busy && active.ws && active.ws.readyState === 1) {
      active.ws.send(JSON.stringify({ type: 'cancel' }));
      active.setChrome('cancelling…', 'busy');
      return;
    }
    if (!input.value.replace(/\s+$/, '') && active.queue.length) {
      var popped = active.queue.pop();
      releaseFiles(popped && popped.files, active.cwd);
      paintQueue();
      paintSessions();
    }
  });

  // Dropping an attachment anywhere — composer chip, queued message, or a
  // whole tab — has to undo the upload: /v1/upload copied the file into
  // the project, where an orphan can end up committed.
  function releaseFiles(files, cwd) {
    (files || []).forEach(function (f) {
      if (!f) return;
      if (f.preview) {
        try { URL.revokeObjectURL(f.preview); } catch (e) {}
      }
      if (!f.copied || !f.path) return;
      var dir = cwd || project;
      if (!dir) return;
      authFetch(paneHTTP() + '/v1/upload?cwd=' + encodeURIComponent(dir) + '&path=' + encodeURIComponent(f.path), { method: 'DELETE' }).catch(function () {});
    });
  }

  function dropPending(i) {
    var f = pending[i];
    if (!f) return;
    pending.splice(i, 1);
    releaseFiles([f], (active && active.cwd) || project);
    paintChips();
    syncSend();
  }

  function paintChips() {
    if (!chipsEl) return;
    chipsEl.textContent = '';
    if (!pending.length) {
      chipsEl.hidden = true;
      return;
    }
    chipsEl.hidden = false;
    pending.forEach(function (f, i) {
      var chip = document.createElement('div');
      chip.className = 'chip';
      if (f.preview) {
        var img = document.createElement('img');
        img.src = f.preview;
        img.alt = '';
        chip.appendChild(img);
      }
      var lab = document.createElement('span');
      lab.className = 'chip-name';
      lab.textContent = f.name || 'file';
      lab.title = f.path || f.name || '';
      var x = document.createElement('button');
      x.type = 'button';
      x.className = 'chip-x';
      x.title = 'Remove attachment';
      x.setAttribute('aria-label', 'Remove ' + (f.name || 'attachment'));
      x.textContent = '×';
      x.addEventListener('click', function (ev) {
        ev.preventDefault();
        ev.stopPropagation();
        dropPending(i);
      });
      chip.appendChild(lab);
      chip.appendChild(x);
      chipsEl.appendChild(chip);
    });
  }

  function addFiles(list) {
    if (!list || !list.length) return;
    var cwd = (active && active.cwd) || project;
    if (!cwd) {
      setStatus('open a project first', 'err');
      return;
    }
    Array.prototype.forEach.call(list, function (file) {
      if (!file) return;
      if (file.size > 20 * 1024 * 1024) {
        setStatus((file.name || 'file') + ' is over 20MB', 'err');
        return;
      }
      var fd = new FormData();
      fd.append('file', file, file.name || 'upload');
      authFetch(paneHTTP() + '/v1/upload?cwd=' + encodeURIComponent(cwd), { method: 'POST', body: fd })
        .then(function (r) {
          if (!r.ok) return r.text().then(function (t) { throw new Error(t || r.statusText); });
          return r.json();
        })
        .then(function (info) {
          pending.push({
            name: info.name || file.name,
            path: info.path,
            mime: info.mime || file.type || '',
            size: info.size || file.size || 0,
            copied: !!info.copied,
            preview: (file.type && file.type.indexOf('image/') === 0) ? URL.createObjectURL(file) : ''
          });
          paintChips();
          syncSend();
        })
        .catch(function (err) {
          setStatus((file.name || 'file') + ': ' + ((err && err.message) || 'upload failed'), 'err');
        });
    });
  }

  function attachPaths(paths) {
    var cwd = (active && active.cwd) || project;
    if (!cwd) {
      setStatus('open a project first', 'err');
      return;
    }
    (paths || []).forEach(function (p) {
      if (!p) return;
      var dup = pending.some(function (f) { return f.path === p || f.name === basename(p); });
      if (dup) return;
      authFetch(paneHTTP() + '/v1/upload?cwd=' + encodeURIComponent(cwd), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path: p })
      })
        .then(function (r) {
          if (!r.ok) return r.text().then(function (t) { throw new Error(t || r.statusText); });
          return r.json();
        })
        .then(function (info) {
          pending.push({
            name: info.name || basename(p),
            path: info.path,
            mime: info.mime || '',
            size: info.size || 0,
            copied: !!info.copied,
            preview: ''
          });
          paintChips();
          syncSend();
        })
        .catch(function (err) {
          setStatus((basename(p) || 'file') + ': ' + ((err && err.message) || 'attach failed'), 'err');
        });
    });
  }

  function pickAttachments() {
    if (pickingProject) return;
    var api = desktopAPI();
    if (api && typeof api.PickFiles === 'function') {
      setPickingProject(true);
      Promise.resolve(api.PickFiles()).then(function (paths) {
        if (paths && paths.length) attachPaths(paths);
      }).catch(function () { setStatus('could not open file picker', 'err'); }).then(function () {
        setPickingProject(false);
      });
      return;
    }
    if (fileInput) fileInput.click();
  }

  if (attachBtn) {
    attachBtn.addEventListener('click', pickAttachments);
  }
  if (fileInput) {
    fileInput.addEventListener('change', function () {
      addFiles(fileInput.files);
      fileInput.value = '';
    });
  }
  var lastDropAt = 0;
  function takeDropPaths(x, y, paths) {
    if (Array.isArray(x) && y == null) paths = x;
    if (!paths && typeof x === 'string') paths = [x];
    if (typeof paths === 'string') paths = paths.split('\n');
    if (!paths || !paths.length) return;
    lastDropAt = Date.now();
    attachPaths(paths);
    showDrop(false);
  }

  function bindNativeDrop() {
    if (window.__paneDropBound) return true;
    var rt = window.runtime;
    if (rt && typeof rt.OnFileDrop === 'function') {
      rt.OnFileDrop(function (x, y, paths) { takeDropPaths(x, y, paths); }, false);
      window.__paneDropBound = true;
      return true;
    }
    return false;
  }
  if (!bindNativeDrop()) {
    var dropTries = 0;
    var dropTimer = setInterval(function () {
      dropTries++;
      if (bindNativeDrop() || dropTries > 50) clearInterval(dropTimer);
    }, 100);
  }

  function hasFiles(e) {
    var dt = e.dataTransfer;
    if (!dt) return false;
    if (dt.types && (dt.types.indexOf ? dt.types.indexOf('Files') !== -1 : dt.types.contains('Files'))) return true;
    return !!(dt.files && dt.files.length);
  }

  var dragDepth = 0;
  function showDrop(on) {
    document.documentElement.classList.toggle('drop', !!on);
    if (dropEl) dropEl.hidden = !on;
    if (!on) dragDepth = 0;
  }
  document.addEventListener('dragenter', function (e) {
    if (!hasFiles(e)) return;
    dragDepth++;
    showDrop(true);
  });
  document.addEventListener('dragleave', function (e) {
    if (!hasFiles(e) && dragDepth === 0) return;
    dragDepth = Math.max(0, dragDepth - 1);
    if (!dragDepth) showDrop(false);
  });
  document.addEventListener('dragover', function (e) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  });
  document.addEventListener('drop', function (e) {
    showDrop(false);
    if (!hasFiles(e)) return;
    e.preventDefault();
    if (Date.now() - lastDropAt < 800) return;
    addFiles(e.dataTransfer.files);
  });

  function desktopAPI() {
    return window.go && window.go.main && window.go.main.App ? window.go.main.App : null;
  }

  function copyPath(path) {
    if (!path) return;
    var api = desktopAPI();
    if (api && api.CopyText) {
      Promise.resolve(api.CopyText(path));
      return;
    }
    if (navigator.clipboard) navigator.clipboard.writeText(path).catch(function () {});
  }

  cwdEl.addEventListener('click', function () {
    var path = active && active.cwd;
    if (!path) return;
    copyPath(path);
    setStatus('copied path', 'ok');
  });

  function liveSessionFor(cwd) {
    cwd = normPath(cwd);
    if (!cwd) return null;
    var lastSid = stored('pane-last-sid:' + cwd);
    var list = sessions.filter(function (s) { return !s.dead && samePath(s.cwd, cwd); });
    var i;
    if (lastSid) {
      for (i = 0; i < list.length; i++) {
        if (list[i].id === lastSid || list[i].resumeID === lastSid) return list[i];
      }
    }
    return list[0] || null;
  }

  function foreignSession(s, msg) {
    var id = msg && msg.session;
    if (!id || !s) return false;
    if (s.id && s.id === id) return false;
    if (s.resumeID && s.resumeID === id) return false;
    return !!(s.id || s.resumeID);
  }

  function deactivateView() {
    sessions.forEach(function (x) {
      if (x.el) x.el.classList.remove('active');
    });
    if (!active) return;
    active = null;
    setStatus('', 'ok');
    document.documentElement.dataset.busy = 'false';
    document.documentElement.setAttribute('aria-busy', 'false');
    syncSend();
    syncRailChrome();
    paintQueue();
    paintCatalog();
    paintUsage();
    syncJump();
    highlightActiveSession();
  }

  function setProject(path) {
    if (!path) return;
    path = normPath(path);
    if (project && !samePath(project, path)) {
      composerHistIdx = -1;
      composerHold = '';
    }
    project = path;
    projectBtn.textContent = projectLabel(path);
    projectBtn.title = path + ' — click to copy';
    try { localStorage.setItem('pane-project', path); } catch (e) {}
    paintCwd(path);
    loadProjects();
    sessPaintKey = '';
    paintSessions();
    var live = liveSessionFor(path);
    if (live) {
      activate(live);
      loadHistory(path);
      return;
    }
    if (active && !samePath(active.cwd, path)) {
      deactivateView();
    }
    loadHistory(path, { resume: true });
  }

  function openProject() {
    if (railLocked()) {
      setStatus('finish the current turn first', 'err');
      return;
    }
    if (pickingProject) return;
    if (window.runtime && typeof window.runtime.EventsEmit === 'function') {
      setPickingProject(true);
      window.runtime.EventsEmit('request-open-project');
      return;
    }
    var api = desktopAPI();
    if (api && typeof api.OpenProject === 'function') {
      setPickingProject(true);
      Promise.resolve(api.OpenProject()).then(function (path) {
        if (path) setProject(path);
      }).catch(function () {}).then(function () { setPickingProject(false); });
      return;
    }
    setPickingProject(true);
    try {
      var path = window.prompt('Project folder', project || '');
      if (path) setProject(path.trim());
    } finally {
      setPickingProject(false);
    }
  }

  projectBtn.addEventListener('click', function () {
    if (project) {
      copyPath(project);
      setStatus('copied path', 'ok');
      return;
    }
    openProject();
  });
  if (changeBtn) changeBtn.addEventListener('click', openProject);
  newBtn.addEventListener('click', function () { newSession(project); });
  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn('project', function (path) { if (path) setProject(path); });
    window.runtime.EventsOn('picker-done', function () { setPickingProject(false); });
    window.runtime.EventsOn('new-session', function () { newSession(project); });
    window.runtime.EventsOn('close-session', function () { closeSession(active); });
    window.runtime.EventsOn('prev-session', function () { cycleSession(-1); });
    window.runtime.EventsOn('next-session', function () { cycleSession(1); });
    window.runtime.EventsOn('prev-project', function () { cycleProject(-1); });
    window.runtime.EventsOn('next-project', function () { cycleProject(1); });
    window.runtime.EventsOn('request-remote-cwd', function () {
      var path = window.prompt('Project folder on the remote machine', project || '');
      if (path) setProject(path.trim());
    });
    window.runtime.EventsOn('request-connect-pane', function () {
      var cur = paneHTTP();
      var u = window.prompt('Pane URL on the tailnet\n(https://machine.ts.net  or  local)', cur);
      if (u == null) return;
      u = String(u).trim();
      var api = desktopAPI();
      if (api && typeof api.SetPaneOrigin === 'function') {
        Promise.resolve(api.SetPaneOrigin(u || 'local')).then(function (got) {
          if (String(got).indexOf('error:') === 0) {
            setStatus(got, 'err');
            return;
          }
          try { localStorage.setItem('pane-url', got); } catch (e) {}
          window.__paneOrigin = got;
          location.reload();
        }).catch(function () { setStatus('could not set pane url', 'err'); });
        return;
      }
      try { localStorage.setItem('pane-url', u); } catch (e) {}
      location.reload();
    });
    window.runtime.EventsOn('pane-origin', function (url) {
      if (!url) return;
      try { localStorage.setItem('pane-url', url); } catch (e) {}
      window.__paneOrigin = url;
      location.reload();
    });
  }

  function sessionNavList() {
    var out = [];
    var seen = {};
    sessions.forEach(function (s) {
      if (s.dead) return;
      if (project && s.cwd && !samePath(s.cwd, project)) return;
      var id = s.id || s.resumeID || ('live-' + s.localId);
      seen[id] = true;
      out.push({ live: s, id: id });
    });
    diskSessions.forEach(function (h) {
      if (!h || !h.id || seen[h.id]) return;
      out.push({ disk: h, id: h.id });
    });
    return out;
  }

  function cycleSession(dir) {
    var list = sessionNavList();
    if (!list.length) {
      if (project) newSession(project);
      return;
    }
    var cur = 0;
    var i;
    for (i = 0; i < list.length; i++) {
      if (active && (list[i].live === active || list[i].id === active.id || list[i].id === active.resumeID)) {
        cur = i;
        break;
      }
    }
    var next = list[(cur + dir + list.length * 20) % list.length];
    if (next.live) activate(next.live);
    else newSession(project, resumeOpts(next.disk));
  }

  function cycleProject(dir) {
    var list = projectListCache.slice();
    if (project) {
      var here = normPath(project);
      if (list.indexOf(here) < 0) list.unshift(here);
    }
    if (!list.length) return;
    var cur = 0;
    var i;
    for (i = 0; i < list.length; i++) {
      if (samePath(list[i], project)) {
        cur = i;
        break;
      }
    }
    setProject(list[(cur + dir + list.length * 20) % list.length]);
  }

  window.addEventListener('keydown', function (e) {
    var meta = e.metaKey || e.ctrlKey;
    var alt = e.altKey;
    var shift = e.shiftKey;
    var brack = e.key === '[' || e.key === ']' || e.code === 'BracketLeft' || e.code === 'BracketRight';
    if (meta && shift && !alt && brack) {
      e.preventDefault();
      cycleSession((e.key === ']' || e.code === 'BracketRight') ? 1 : -1);
      return;
    }
    if (meta && alt && !shift && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
      e.preventDefault();
      cycleProject(e.key === 'ArrowRight' ? 1 : -1);
      return;
    }
    var key = (e.code === 'KeyO' || e.key === 'o' || e.key === 'O');
    var n = (e.code === 'KeyN' || e.key === 'n' || e.key === 'N');
    var w = (e.code === 'KeyW' || e.key === 'w' || e.key === 'W');
    if (isDesktop()) {
      // Native File menu owns ⌘O / ⌘N / ⌘W. Do not steal them here.
      return;
    }
    if ((e.metaKey || e.ctrlKey) && n && !e.shiftKey) {
      e.preventDefault();
      newSession(project);
    }
    if ((e.metaKey || e.ctrlKey) && w && !e.shiftKey) {
      e.preventDefault();
      closeSession(active);
    }
    if ((e.metaKey || e.ctrlKey) && key && !e.shiftKey) {
      e.preventDefault();
      openProject();
    }
  });

  if (jumpBtn) {
    jumpBtn.addEventListener('click', function () {
      if (active) active.scroll(true);
    });
  }

  window.addEventListener('resize', function () {
    if (!active) return;
    if (autoScroll && active.stick) active.scroll();
    else syncJump();
  });

  function stripHash(href) {
    var i = String(href).indexOf('#');
    return i < 0 ? String(href) : String(href).slice(0, i);
  }

  // True only for a link that moves inside this document — the #anchor a
  // markdown table of contents emits. Everything else, relative included,
  // resolves to some other document.
  function sameDocument(u) {
    return !!(u && u.hash) && stripHash(u.href) === stripHash(location.href);
  }

  // An in-page anchor has to scroll, not open a window. renderMd strips
  // `id`, so a heading usually answers to `name` instead; when nothing
  // matches, the click is simply spent, which is still not a navigation.
  function scrollToAnchor(hash) {
    var id = String(hash || '').replace(/^#/, '');
    try { id = decodeURIComponent(id); } catch (e) {}
    if (!id) return;
    var el = document.getElementById(id);
    if (!el) {
      var named = document.getElementsByName(id);
      el = (named && named[0]) || null;
    }
    if (el && el.scrollIntoView) el.scrollIntoView({ block: 'start' });
  }

  document.addEventListener('click', function (e) {
    var t = e.target;
    var a = t && t.closest ? t.closest('a[href]') : null;
    if (!a) return;
    var href = a.getAttribute('href') || '';
    var u = null;
    try { u = new URL(href, location.href); } catch (err) { u = null; }
    // Every anchor on this page came out of agent markdown, so the click
    // is taken first and answered afterwards: letting the browser follow
    // one would unload the app, cancelling every running turn, and could
    // hit a pane endpoint that acts.
    e.preventDefault();
    e.stopPropagation();
    if (!u) return;
    if (sameDocument(u)) {
      scrollToAnchor(u.hash);
      return;
    }
    if (externalSchemes.indexOf(u.protocol) < 0) return;
    // A same-origin URL that is not an in-page anchor is the navigation
    // this guard exists to stop; handing it to openExternal would only
    // move the same load into a second window aimed at the pane. Only the
    // web schemes get this test — mailto: and friends report origin
    // "null", which a page on a custom scheme would match.
    var web = u.protocol === 'http:' || u.protocol === 'https:';
    if (web && u.origin === location.origin) return;
    openExternal(u.href);
  }, true);

  function boot() {
    var q = new URLSearchParams(location.search);
    var qCwd = q.get('cwd') || '';
    var qSid = q.get('sid') || '';
    var qTitle = q.get('title') || '';
    try {
      var pending = JSON.parse(localStorage.getItem('pane-pending-session') || 'null');
      if (pending && pending.sid) {
        qCwd = qCwd || pending.cwd || '';
        qSid = qSid || pending.sid || '';
        qTitle = qTitle || pending.title || '';
        localStorage.removeItem('pane-pending-session');
      }
    } catch (e) {}
    var saved = qCwd;
    if (!saved) {
      try { saved = localStorage.getItem('pane-project') || ''; } catch (e) {}
    }
    function start() {
      fetchJSON(paneHTTP() + '/meta')
        .then(function (meta) {
          // The phone is a window onto this pane, not its own last-project.
          // URL/pending sid still wins; otherwise grok's last session does,
          // including over this WebView's localStorage (a prior sim boot
          // that landed on HOME or another project would otherwise stick).
          if (!qCwd && !qSid && meta && meta.lastCwd) {
            saved = meta.lastCwd;
            if (meta.lastSid) qSid = meta.lastSid;
            if (!qTitle && meta.lastTitle) qTitle = meta.lastTitle;
          }
          if (!saved) saved = (meta && meta.cwd) || '';
          resumeBoot(saved);
        })
        .catch(function () { resumeBoot(saved); });
      loadRemote();
      loadProjects();
      input.focus();
    }
    function resumeBoot(path) {
      path = normPath(path);
      if (qSid) {
        if (path) {
          project = path;
          projectBtn.textContent = basename(path) || path;
          projectBtn.title = path + ' — click to copy';
          try { localStorage.setItem('pane-project', path); } catch (e) {}
          loadHistory(path);
        }
        newSession(path || project, { sid: qSid, title: qTitle });
        return;
      }
      if (path) setProject(path);
    }
    // The desktop app serves its own page, so there is no token in it.
    // Ask the app for the local server's token before anything connects.
    function withToken(done) {
      var api = desktopAPI();
      if (paneToken || !api || typeof api.PaneToken !== 'function') {
        done();
        return;
      }
      Promise.resolve(api.PaneToken()).then(function (t) {
        setPaneToken(t);
        done();
      }).catch(function () { done(); });
    }

    var api = desktopAPI();
    if (api && typeof api.PaneOrigin === 'function') {
      Promise.resolve(api.PaneOrigin()).then(function (o) {
        if (o) {
          window.__paneOrigin = o;
          try { localStorage.setItem('pane-url', o); } catch (e) {}
        }
        withToken(start);
      }).catch(function () { withToken(start); });
      return;
    }
    withToken(start);
  }
  boot();

  (function bindRailResize() {
    var split = document.getElementById('rail-split');
    var shell = document.getElementById('shell');
    if (!split || !shell) return;
    var minW = 220;
    var maxW = 480;
    var defW = 280;
    var dragging = false;

    function clamp(n) {
      var cap = Math.min(maxW, Math.floor(window.innerWidth * 0.5));
      if (!(cap >= minW)) cap = minW;
      if (!(n >= minW)) return minW;
      if (n > cap) return cap;
      return n;
    }
    function current() {
      return parseInt(getComputedStyle(document.documentElement).getPropertyValue('--rail-w'), 10) || defW;
    }
    function apply(n) {
      n = clamp(Math.round(n));
      document.documentElement.style.setProperty('--rail-w', n + 'px');
      split.setAttribute('aria-valuenow', String(n));
      return n;
    }
    function persist(n) {
      store('pane-rail-w', String(n));
    }
    apply(parseInt(stored('pane-rail-w'), 10) || current());

    function onMove(e) {
      if (!dragging) return;
      apply(e.clientX - shell.getBoundingClientRect().left);
    }
    function onUp() {
      if (!dragging) return;
      dragging = false;
      document.body.classList.remove('rail-drag');
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      persist(current());
    }
    split.addEventListener('pointermove', onMove);
    split.addEventListener('pointerup', onUp);
    split.addEventListener('pointercancel', onUp);
    split.addEventListener('pointerdown', function (e) {
      if (e.button !== 0) return;
      e.preventDefault();
      dragging = true;
      document.body.classList.add('rail-drag');
      if (split.focus) split.focus();
      if (split.setPointerCapture) {
        try { split.setPointerCapture(e.pointerId); } catch (err) {}
      }
      window.addEventListener('pointermove', onMove);
      window.addEventListener('pointerup', onUp);
    });
    split.addEventListener('dblclick', function () {
      persist(apply(defW));
    });
    split.addEventListener('keydown', function (e) {
      var cur = current();
      var step = e.shiftKey ? 32 : 16;
      if (e.key === 'ArrowLeft') {
        e.preventDefault();
        persist(apply(cur - step));
      } else if (e.key === 'ArrowRight') {
        e.preventDefault();
        persist(apply(cur + step));
      } else if (e.key === 'Home') {
        e.preventDefault();
        persist(apply(minW));
      } else if (e.key === 'End') {
        e.preventDefault();
        persist(apply(maxW));
      }
    });
    window.addEventListener('resize', function () {
      apply(parseInt(stored('pane-rail-w'), 10) || current());
    });
  })();

  (function bindRailMenu() {
    var btn = document.getElementById('menu');
    var backdrop = document.getElementById('rail-backdrop');
    var rail = document.getElementById('rail');
    function setOpen(on) {
      document.documentElement.classList.toggle('rail-open', !!on);
      if (btn) btn.setAttribute('aria-expanded', on ? 'true' : 'false');
    }
    function close() { setOpen(false); }
    if (btn) {
      btn.addEventListener('click', function () {
        setOpen(!document.documentElement.classList.contains('rail-open'));
      });
    }
    if (backdrop) backdrop.addEventListener('click', close);
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') close();
    });
    if (rail) {
      rail.addEventListener('click', function (e) {
        var t = e.target && e.target.closest && e.target.closest('button');
        if (!t || t.classList.contains('sess-close') || t.id === 'rail-split') return;
        close();
      }, true);
    }
    window.addEventListener('resize', function () {
      if (window.innerWidth > 900 && window.innerHeight > 520) close();
    });
    function syncViewport() {
      var vv = window.visualViewport;
      if (!vv) return;
      document.documentElement.style.setProperty('--vvh', Math.round(vv.height) + 'px');
    }
    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', syncViewport);
      window.visualViewport.addEventListener('scroll', syncViewport);
      syncViewport();
    }
  })();

  document.addEventListener('visibilitychange', function () {
    if (!document.hidden) kickReconnects();
  });
  window.addEventListener('online', kickReconnects);
  window.addEventListener('pageshow', kickReconnects);
})();
