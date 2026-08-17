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

  function paneWS(cwd) {
    var http = paneHTTP();
    var u;
    try { u = new URL(http); } catch (e) { u = new URL('http://127.0.0.1:7420'); }
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
    u.pathname = '/ws';
    u.search = cwd ? ('cwd=' + encodeURIComponent(cwd)) : '';
    return u.toString();
  }

  var themeBtn = document.getElementById('theme');
  var thoughtsBtn = document.getElementById('thoughts');
  var status = document.getElementById('status');
  var cwdEl = document.getElementById('cwd');
  var input = document.getElementById('in');
  var sendBtn = document.getElementById('send');
  var logRoot = document.getElementById('log');
  var sessionsEl = document.getElementById('sessions');
  var treeEl = document.getElementById('tree');
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
    return active && !active.busy && active.ws && active.ws.readyState === 1 && !!input.value.replace(/\s+$/, '');
  }

  function syncSend() { sendBtn.disabled = !canSend(); }

  function setBusy(on) {
    if (active) active.busy = on;
    document.documentElement.dataset.busy = on ? 'true' : 'false';
    document.documentElement.setAttribute('aria-busy', on ? 'true' : 'false');
    syncSend();
    paintSessions();
    if (!on) input.focus();
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
      b.textContent = s.busy ? s.title + ' ·' : s.title;
      b.title = (s.busy ? 'working… — ' : '') + 'double-click to rename';
      b.disabled = s.busy && s !== active;
      b.addEventListener('click', function () {
        if (s === active) return;
        if (s.busy || railLocked()) return;
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
      x.title = s.busy ? 'working…' : 'Close session';
      x.textContent = '×';
      x.disabled = s.busy;
      x.addEventListener('click', function (ev) {
        ev.stopPropagation();
        if (s.busy || railLocked()) return;
        closeSession(s);
      });
      row.appendChild(b);
      row.appendChild(x);
      sessionsEl.appendChild(row);
    });
  }

  function activate(s) {
    if (!s || (railLocked() && s !== active)) return;
    active = s;
    sessions.forEach(function (x) {
      if (x.el) x.el.classList.toggle('active', x === s);
    });
    paintSessions();
    paintCwd(s.cwd);
    setBusy(s.busy);
    setStatus(s.statusText || (s.busy ? 'working…' : 'ready'), s.statusCls || (s.busy ? 'busy' : 'ok'));
    s.scroll();
    input.focus();
  }

  function Session(cwd) {
    this.localId = ++n;
    this.id = '';
    this.cwd = cwd || project || '';
    this.title = 'Session ' + this.localId;
    this.named = false;
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
    var ws = new WebSocket(paneWS(s.cwd));
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
          s.id = msg.session || s.id;
          if (msg.cwd) s.cwd = msg.cwd;
          if (s === active) paintCwd(s.cwd);
          s.busy = false;
          s.setChrome('ready', 'ok');
          if (s === active) setBusy(false);
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

  function newSession(cwd) {
    if (railLocked()) return;
    var s = new Session(cwd || project);
    sessions.push(s);
    s.connect();
    activate(s);
    return s;
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
  }

  function send() {
    var text = input.value.replace(/\s+$/, '');
    if (!text || !active || active.busy || !active.ws || active.ws.readyState !== 1) return;
    input.value = '';
    grow();
    if (!active.named && active.title.indexOf('Session ') === 0) {
      active.title = text.length > 28 ? text.slice(0, 28) + '…' : text;
      paintSessions();
    }
    active.addYou(text);
    active.startedReply = false;
    active.agentBuf = '';
    active.agentEl = null;
    active.thoughtBuf = '';
    active.thoughtEl = null;
    active.busy = true;
    setBusy(true);
    active.setChrome('working…', 'busy');
    active.ws.send(JSON.stringify({ type: 'in', text: text }));
  }

  sendBtn.addEventListener('click', send);
  input.addEventListener('input', function () { grow(); syncSend(); });
  input.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
      return;
    }
    if (e.key === 'Escape' && active && active.busy && active.ws && active.ws.readyState === 1) {
      active.ws.send(JSON.stringify({ type: 'cancel' }));
      active.setChrome('cancelling…', 'busy');
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
    loadTree(path, treeEl, 0);
    if (!active || active.cwd !== path) newSession(path);
    else paintCwd(path);
  }

  function loadTree(path, into, depth) {
    if (depth === 0) into.textContent = '';
    fetch(paneHTTP() + '/v1/tree?path=' + encodeURIComponent(path))
      .then(function (r) { return r.ok ? r.json() : []; })
      .then(function (ents) {
        ents.forEach(function (e) {
          var b = document.createElement('button');
          b.type = 'button';
          b.className = 'file' + (e.dir ? ' dir' : '');
          b.style.paddingLeft = (0.35 + depth * 0.7) + 'rem';
          b.textContent = (e.dir ? '▸ ' : '') + e.name;
          b.title = e.path;
          b.addEventListener('click', function (ev) {
            ev.stopPropagation();
            if (e.dir) {
              if (b.dataset.open === '1') {
                b.dataset.open = '';
                b.textContent = '▸ ' + e.name;
                while (b.nextSibling && b.nextSibling.dataset && b.nextSibling.dataset.parent === e.path) {
                  b.parentNode.removeChild(b.nextSibling);
                }
              } else {
                b.dataset.open = '1';
                b.textContent = '▾ ' + e.name;
                var wrap = document.createElement('div');
                wrap.dataset.parent = e.path;
                b.after(wrap);
                loadTree(e.path, wrap, depth + 1);
              }
              return;
            }
            var rel = project && e.path.indexOf(project) === 0 ? e.path.slice(project.length).replace(/^[/\\]/, '') : e.path;
            input.value = (input.value ? input.value.replace(/\s+$/, '') + ' ' : '') + rel;
            grow();
            syncSend();
            input.focus();
          });
          into.appendChild(b);
        });
      })
      .catch(function () {});
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
