/* Grok Pane UI. Talks to the pane server over HTTP + WS. ACP stays in Go. */
(function () {
  function currentTheme() {
    return document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light';
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
        html = '<p>' + raw.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\n/g, '<br>') + '</p>';
      }
    } catch (e) {
      html = '<p>' + raw.replace(/&/g, '&amp;').replace(/</g, '&lt;') + '</p>';
    }
    if (window.DOMPurify) return DOMPurify.sanitize(html, { USE_PROFILES: { html: true } });
    return html;
  }

  function isDesktop() {
    return !!(window.runtime || window.wails || (window.go && window.go.main));
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
    return u.toString();
  }

  function stored(k) {
    try { return localStorage.getItem(k) || ''; } catch (e) { return ''; }
  }
  function store(k, v) {
    try { if (v) localStorage.setItem(k, v); } catch (e) {}
  }

  var themeBtn = document.getElementById('theme');
  var thoughtsBtn = document.getElementById('thoughts');
  var status = document.getElementById('status');
  var cwdEl = document.getElementById('cwd');
  var input = document.getElementById('in');
  var sendBtn = document.getElementById('send');
  var queueEl = document.getElementById('queue');
  var modelEl = document.getElementById('model');
  var effortEl = document.getElementById('effort');
  var usageEl = document.getElementById('usage');
  var usageRing = document.getElementById('usage-ring');
  var logRoot = document.getElementById('log');
  var sessionsEl = document.getElementById('sessions');
  var historyEl = document.getElementById('history');
  var projectBtn = document.getElementById('project');
  var changeBtn = document.getElementById('change-project');
  var newBtn = document.getElementById('new-session');
  var showThoughts = false;
  var project = '';
  var sessions = [];
  var active = null;
  var n = 0;

  function paintTheme() {
    themeBtn.textContent = currentTheme() === 'dark' ? 'Light' : 'Dark';
  }

  function paintThoughtsBtn() {
    thoughtsBtn.setAttribute('aria-pressed', showThoughts ? 'true' : 'false');
    thoughtsBtn.textContent = showThoughts ? 'Thoughts on' : 'Thoughts';
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
  paintThoughtsBtn();
  thoughtsBtn.addEventListener('click', function () {
    showThoughts = !showThoughts;
    paintThoughtsBtn();
    sessions.forEach(function (s) { s.showThoughts(showThoughts); });
  });

  function setStatus(text, cls) {
    status.textContent = text;
    status.className = cls || '';
  }

  function canSend() {
    return active && !active.dead && active.ws && active.ws.readyState === 1 && !!input.value.replace(/\s+$/, '');
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
    paintSessions();
    paintQueue();
    if (!on) input.focus();
  }

  function setBusy(on) {
    if (active) active.busy = on;
    syncBusyChrome();
  }

  function railLocked() {
    return !!(active && active.busy);
  }

  function grow() {
    input.style.height = 'auto';
    input.style.height = Math.min(input.scrollHeight, 144) + 'px';
  }

  function basename(p) {
    if (!p) return '';
    var parts = p.replace(/\\/g, '/').split('/');
    return parts[parts.length - 1] || p;
  }

  function paintCwd(path) {
    if (!path) {
      cwdEl.hidden = true;
      return;
    }
    cwdEl.hidden = false;
    cwdEl.textContent = path;
    cwdEl.title = 'copy ' + path;
  }

  var renaming = null;

  function commitRename(s, val) {
    val = String(val || '').replace(/\s+/g, ' ').trim();
    if (val) {
      s.title = val;
      s.named = true;
    }
    renaming = null;
    paintSessions();
  }

  function startRename(s) {
    if (!s || renaming === s) return;
    renaming = s;
    paintSessions();
  }

  function paintSessions() {
    if (renaming && sessionsEl.querySelector('.sess-edit')) return;
    sessionsEl.textContent = '';
    sessions.forEach(function (s) {
      var row = document.createElement('div');
      row.className = 'sess-row' + (s.busy ? ' locked' : '');
      if (renaming === s) {
        var inp = document.createElement('input');
        inp.type = 'text';
        inp.className = 'sess-edit';
        inp.value = s.title;
        inp.setAttribute('aria-label', 'Session name');
        inp.addEventListener('keydown', function (e) {
          if (e.key === 'Enter') {
            e.preventDefault();
            commitRename(s, inp.value);
          }
          if (e.key === 'Escape') {
            e.preventDefault();
            renaming = null;
            paintSessions();
          }
          e.stopPropagation();
        });
        inp.addEventListener('blur', function () { commitRename(s, inp.value); });
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
      b.textContent = s.title + (s.busy ? ' ·' : '') + (s.queue && s.queue.length ? ' +' + s.queue.length : '');
      b.title = (s.busy ? 'working… — ' : '') + (s.queue && s.queue.length ? s.queue.length + ' queued — ' : '') + 'double-click to rename';
      b.addEventListener('click', function () {
        if (s === active) return;
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
  }

  function activate(s) {
    if (!s) return;
    active = s;
    sessions.forEach(function (x) {
      if (x.el) x.el.classList.toggle('active', x === s);
    });
    paintCwd(s.cwd);
    setStatus(s.statusText || (s.busy ? 'working…' : 'ready'), s.statusCls || (s.busy ? 'busy' : 'ok'));
    syncBusyChrome();
    paintQueue();
    paintCatalog();
    paintUsage();
    s.scroll();
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
    this.statusText = 'connecting…';
    this.statusCls = '';
    this.reconnects = 0;
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
    this.el = document.createElement('div');
    this.el.className = 'log-slot';
    this.el.setAttribute('role', 'log');
    logRoot.appendChild(this.el);
  }

  Session.prototype.scroll = function () {
    this.el.scrollTop = this.el.scrollHeight;
  };

  Session.prototype.addYou = function (text) {
    var wrap = document.createElement('div');
    wrap.className = 'msg you';
    var who = document.createElement('div');
    who.className = 'who';
    who.textContent = 'you';
    var body = document.createElement('div');
    body.className = 'body';
    body.textContent = text;
    wrap.appendChild(who);
    wrap.appendChild(body);
    this.el.appendChild(wrap);
    this.agentBuf = '';
    this.agentEl = null;
    this.scroll();
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
    var wrap = document.createElement('div');
    wrap.className = 'msg tool';
    wrap.textContent = '· ' + title;
    this.el.appendChild(wrap);
    this.scroll();
  };

  Session.prototype.addErr = function (text) {
    var wrap = document.createElement('div');
    wrap.className = 'msg err';
    wrap.textContent = text || 'error';
    this.el.appendChild(wrap);
    this.scroll();
  };

  Session.prototype.setChrome = function (text, cls) {
    this.statusText = text;
    this.statusCls = cls || '';
    if (this === active) setStatus(text, cls);
  };

  Session.prototype.connect = function () {
    var s = this;
    s.busy = true;
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
      if (s === active) setBusy(true);
      s.setChrome('disconnected', 'err');
      s.reconnects++;
      setTimeout(function () { s.connect(); }, Math.min(8000, 400 * s.reconnects));
    };
    ws.onmessage = function (ev) {
      var msg;
      try { msg = JSON.parse(ev.data); } catch (e) { return; }
      switch (msg.type) {
        case 'ready':
          s.reconnects = 0;
          s.seenReady = true;
          s.id = msg.session || s.id;
          if (s.id) s.resumeID = s.id;
          if (msg.cwd) s.cwd = msg.cwd;
          if (s === active) paintCwd(s.cwd);
          s.busy = false;
          s.setChrome('ready', 'ok');
          if (s === active) setBusy(false);
          else paintSessions();
          applyCatalog(s, msg);
          if (s === active) paintCatalog();
          refreshUsage(s);
          loadHistory(s.cwd);
          flushQueue(s);
          break;
        case 'you':
          s.addYou(msg.text || '');
          break;
        case 'out':
          if (!s.startedReply) s.startedReply = true;
          s.addOut(msg.text || '');
          break;
        case 'thought':
          s.addThought(msg.text || '');
          break;
        case 'tool':
          renderTool(s, msg);
          break;
        case 'err':
          s.addErr(msg.text || 'error');
          s.setChrome(msg.text || 'error', 'err');
          break;
        case 'busy':
          s.busy = true;
          s.startedReply = false;
          s.tools = {};
          s.agentBuf = '';
          s.agentEl = null;
          s.thoughtBuf = '';
          s.thoughtEl = null;
          s.setChrome('working…', 'busy');
          if (s === active) setBusy(true);
          break;
        case 'idle':
          s.busy = false;
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

  function renderTool(s, msg) {
    var id = msg.id || msg.text || 'tool';
    var prev = s.tools[id];
    var st = msg.status || '';
    var title = msg.text || 'tool';
    if (!prev) {
      s.tools[id] = { title: title, status: st };
      s.addTool(title);
      return;
    }
    if (st && st !== prev.status && (st === 'completed' || st === 'failed' || st === 'cancelled')) {
      prev.status = st;
    }
  }

  function newSession(cwd, opts) {
    opts = opts || {};
    if (opts.sid) {
      var existing = sessions.filter(function (x) { return x.id === opts.sid || x.resumeID === opts.sid; })[0];
      if (existing) {
        activate(existing);
        return existing;
      }
    }
    var s = new Session(cwd || project);
    if (opts.sid) s.resumeID = opts.sid;
    if (opts.title) {
      s.title = opts.title;
      s.named = true;
    }
    sessions.push(s);
    s.connect();
    activate(s);
    return s;
  }

  function loadHistory(cwd) {
    if (!historyEl || !cwd) return;
    fetch(paneHTTP() + '/v1/sessions?cwd=' + encodeURIComponent(cwd))
      .then(function (r) { return r.ok ? r.json() : []; })
      .then(function (list) {
        historyEl.textContent = '';
        var open = {};
        sessions.forEach(function (s) {
          if (s.id) open[s.id] = true;
          if (s.resumeID) open[s.resumeID] = true;
        });
        (list || []).forEach(function (h) {
          if (!h || !h.id || open[h.id]) return;
          var row = document.createElement('div');
          row.className = 'sess-row';
          var b = document.createElement('button');
          b.type = 'button';
          b.className = 'hist';
          var title = h.title || h.id;
          b.textContent = title;
          var when = (h.updated || '').replace('T', ' ').replace(/\.\d+Z$/, 'Z');
          b.title = (h.id || '') + (when ? '\n' + when : '');
          b.addEventListener('click', function () {
            newSession(h.cwd || cwd, { sid: h.id, title: title });
          });
          var x = document.createElement('button');
          x.type = 'button';
          x.className = 'sess-close';
          x.title = 'Delete from history permanently';
          x.textContent = '×';
          x.addEventListener('click', function (ev) {
            ev.stopPropagation();
            wipeSession(h.cwd || cwd, h.id, title, null);
          });
          row.appendChild(b);
          row.appendChild(x);
          historyEl.appendChild(row);
        });
        if (!historyEl.childNodes.length) {
          var empty = document.createElement('div');
          empty.className = 'hist';
          empty.style.cursor = 'default';
          empty.textContent = 'no past sessions';
          historyEl.appendChild(empty);
        }
      })
      .catch(function () {});
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
    if (s === active) active = null;
    s.shutdown();
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
      fetch(paneHTTP() + '/v1/sessions?cwd=' + encodeURIComponent(cwd) + '&id=' + encodeURIComponent(id), { method: 'DELETE' })
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
    var w = this.ws;
    this.ws = null;
    if (w) {
      try { w.close(); } catch (e) {}
    }
    if (this.el && this.el.parentNode) this.el.parentNode.removeChild(this.el);
  };

  function closeSession(s) {
    s = s || active;
    if (!s || s.busy) return;
    var i = sessions.indexOf(s);
    if (i < 0) return;
    sessions.splice(i, 1);
    s.shutdown();
    if (s === active) active = null;
    if (sessions.length) activate(sessions[Math.min(i, sessions.length - 1)]);
    else newSession(project);
    paintSessions();
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

  function paintCatalog() {
    if (!modelEl || !effortEl) return;
    var s = active;
    var models = (s && s.models) ? s.models : [];
    var cur = s && s.model ? s.model : stored('pane-model');
    var eff = s && s.effort ? s.effort : stored('pane-effort');
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
    var title = size ? (Math.round(pct * 100) + '% · ' + fmtNum(used) + ' / ' + fmtNum(size)) : 'Context usage';
    usageEl.title = title;
    usageEl.setAttribute('aria-label', title);
  }

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
    fetch(paneHTTP() + '/v1/usage?cwd=' + encodeURIComponent(s.cwd) + '&id=' + encodeURIComponent(s.id))
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (u) {
        if (!u) return;
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
    q.forEach(function (text, i) {
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
        var cur = input.value.replace(/\s+$/, '');
        active.queue.splice(i, 1);
        if (cur) active.queue.splice(i, 0, cur);
        input.value = text;
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
        active.queue.splice(i, 1);
        paintQueue();
        paintSessions();
      });
      row.appendChild(mark);
      row.appendChild(t);
      row.appendChild(x);
      queueEl.appendChild(row);
    });
  }

  function dispatch(s, text) {
    if (!s || s.dead || !s.ws || s.ws.readyState !== 1) return false;
    if (!s.named && s.title.indexOf('Session ') === 0) {
      s.title = text.length > 28 ? text.slice(0, 28) + '…' : text;
    }
    s.addYou(text);
    s.startedReply = false;
    s.agentBuf = '';
    s.agentEl = null;
    s.thoughtBuf = '';
    s.thoughtEl = null;
    s.busy = true;
    if (s === active) setBusy(true);
    else paintSessions();
    s.setChrome('working…', 'busy');
    s.ws.send(JSON.stringify({ type: 'in', text: text }));
    return true;
  }

  function flushQueue(s) {
    if (!s || s.dead || s.busy || !s.ws || s.ws.readyState !== 1) return;
    if (!s.queue.length) {
      if (s === active) paintQueue();
      return;
    }
    var text = s.queue.shift();
    if (s === active) paintQueue();
    dispatch(s, text);
  }

  function send() {
    var text = input.value.replace(/\s+$/, '');
    if (!text || !active || active.dead || !active.ws || active.ws.readyState !== 1) return;
    input.value = '';
    grow();
    if (active.busy) {
      if (active.queue.length >= 20) {
        setStatus('queue full (20)', 'err');
        input.value = text;
        grow();
        syncSend();
        return;
      }
      active.queue.push(text);
      paintQueue();
      paintSessions();
      syncSend();
      return;
    }
    dispatch(active, text);
  }

  sendBtn.addEventListener('click', send);
  input.addEventListener('input', function () { grow(); syncSend(); });
  input.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
      return;
    }
    if (e.key !== 'Escape' || !active) return;
    if (active.busy && active.ws && active.ws.readyState === 1) {
      active.ws.send(JSON.stringify({ type: 'cancel' }));
      active.setChrome('cancelling…', 'busy');
      return;
    }
    if (!input.value.replace(/\s+$/, '') && active.queue.length) {
      active.queue.pop();
      paintQueue();
      paintSessions();
    }
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

  function reveal(path) {
    if (!path) return;
    var api = desktopAPI();
    if (api && api.Reveal) {
      Promise.resolve(api.Reveal(path)).catch(function () {});
      return true;
    }
    return false;
  }

  cwdEl.addEventListener('click', function () {
    var path = active && active.cwd;
    if (!path) return;
    copyPath(path);
    if (!reveal(path)) {
      setStatus('copied path', 'ok');
    }
  });

  function setProject(path) {
    if (!path) return;
    project = path;
    projectBtn.textContent = basename(path) || path;
    projectBtn.title = path + ' — click to show in Finder';
    try { localStorage.setItem('pane-project', path); } catch (e) {}
    loadHistory(path);
    if (!active || active.cwd !== path) newSession(path);
    else paintCwd(path);
  }

  function openProject() {
    if (railLocked()) return;
    if (window.runtime && typeof window.runtime.EventsEmit === 'function') {
      window.runtime.EventsEmit('request-open-project');
      return;
    }
    var api = desktopAPI();
    if (api && typeof api.OpenProject === 'function') {
      Promise.resolve(api.OpenProject()).then(function (path) {
        if (path) setProject(path);
      }).catch(function () {});
      return;
    }
    var path = window.prompt('Project folder', project || '');
    if (path) setProject(path.trim());
  }

  projectBtn.addEventListener('click', function () {
    if (project && reveal(project)) return;
    openProject();
  });
  if (changeBtn) changeBtn.addEventListener('click', openProject);
  newBtn.addEventListener('click', function () { newSession(project); });
  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn('project', function (path) { if (path) setProject(path); });
    window.runtime.EventsOn('new-session', function () { newSession(project); });
    window.runtime.EventsOn('close-session', function () { closeSession(active); });
  }

  window.addEventListener('keydown', function (e) {
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

  window.addEventListener('resize', function () {
    if (active) active.scroll();
  });

  function boot() {
    var saved = '';
    try { saved = localStorage.getItem('pane-project') || ''; } catch (e) {}
    fetch(paneHTTP() + '/meta')
      .then(function (r) { return r.json(); })
      .then(function (meta) {
        if (!saved) saved = meta.cwd || '';
        if (saved) setProject(saved);
        else newSession('');
      })
      .catch(function () {
        if (saved) setProject(saved);
        else newSession('');
      });
    input.focus();
  }
  boot();
})();
