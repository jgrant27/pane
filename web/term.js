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
    if (window.DOMPurify) {
      return DOMPurify.sanitize(html, { USE_PROFILES: { html: true }, ADD_ATTR: ['target', 'rel'] });
    }
    return html;
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
  var sessionsEl = document.getElementById('sessions');
  var projectsEl = document.getElementById('projects');
  var diskSessions = [];
  var remoteEl = document.getElementById('remote');
  var projectBtn = document.getElementById('project');
  var changeBtn = document.getElementById('change-project');
  var newBtn = document.getElementById('new-session');
  var showThoughts = false;
  var project = '';
  var sessions = [];
  var active = null;
  var n = 0;
  var pickingProject = false;

  function setPickingProject(on) {
    pickingProject = !!on;
    if (changeBtn) {
      changeBtn.disabled = pickingProject;
      changeBtn.setAttribute('aria-busy', pickingProject ? 'true' : 'false');
    }
  }

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
    text = text || '';
    cls = cls || '';
    status.textContent = text;
    status.className = cls;
    if (liveEl) {
      liveEl.hidden = !text || (cls === 'ok' && text === 'ready');
    }
  }

  function canSend() {
    return active && !active.dead && active.ws && active.ws.readyState === 1 &&
      (!!input.value.replace(/\s+$/, '') || pending.length > 0);
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
    refreshSessionRow(active);
    paintQueue();
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
      return;
    }
    cwdEl.hidden = false;
    cwdEl.textContent = path;
    cwdEl.title = 'copy ' + path;
  }

  var renaming = null;
  var renamingProject = null;
  var projectNames = {};

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
        fetch(paneHTTP() + '/v1/rename?cwd=' + encodeURIComponent(cwd) + '&id=' + encodeURIComponent(id), {
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
        var inp2 = document.createElement('input');
        inp2.type = 'text';
        inp2.className = 'sess-edit';
        inp2.value = h.title || h.id;
        inp2.setAttribute('aria-label', 'Session name');
        inp2.addEventListener('keydown', function (e) {
          if (e.key === 'Enter') {
            e.preventDefault();
            commitRename(renaming, inp2.value);
          }
          if (e.key === 'Escape') {
            e.preventDefault();
            renaming = null;
            paintSessions();
          }
          e.stopPropagation();
        });
        inp2.addEventListener('blur', function () { commitRename(renaming, inp2.value); });
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

  function activate(s) {
    if (!s) return;
    if (s === active) {
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
    paintQueue();
    paintCatalog();
    paintUsage();
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
    this.usage = null;
    this.talked = false;
    this.toolsBox = null;
    this.toolsList = null;
    this.toolsSum = null;
    this.toolCount = 0;
    this.askEl = null;
    this.asking = false;
    this.el = document.createElement('div');
    this.el.className = 'log-slot';
    this.el.setAttribute('role', 'log');
    logRoot.appendChild(this.el);
  }

  Session.prototype.scroll = function () {
    this.el.scrollTop = this.el.scrollHeight;
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
    if (!questions.length) {
      questions = [{ question: 'Grok has a question.', options: [] }];
    }
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
        var b = document.createElement('button');
        b.type = 'button';
        b.className = 'ask-opt';
        var lab = document.createElement('div');
        lab.className = 'ask-label';
        lab.textContent = (oi + 1) + '. ' + (o.label || 'option');
        b.appendChild(lab);
        if (o.description) {
          var d = document.createElement('div');
          d.className = 'ask-desc';
          d.textContent = o.description;
          b.appendChild(d);
        }
        b.addEventListener('click', function () {
          if (!s.asking) return;
          if (multi) {
            var i = chosen[qi].indexOf(o.label);
            if (i >= 0) chosen[qi].splice(i, 1);
            else chosen[qi].push(o.label);
            b.classList.toggle('on', i < 0);
            return;
          }
          chosen[qi] = [o.label];
          var siblings = opts.querySelectorAll('.ask-opt');
          for (var j = 0; j < siblings.length; j++) siblings[j].classList.toggle('on', siblings[j] === b);
          if (questions.length === 1) finish(collect(), 'accept');
        });
        opts.appendChild(b);
      });
      block.appendChild(opts);
      wrap.appendChild(block);
    });
    var actions = document.createElement('div');
    actions.className = 'ask-actions';
    var needsSubmit = questions.length > 1 || multiFlags.some(function (m) { return m; });
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
        return { question: q.question || '', selected: (chosen[i] || []).slice() };
      });
    }

    function finish(answers, action) {
      if (!s.asking) return;
      s.asking = false;
      wrap.classList.add('done');
      var buttons = wrap.querySelectorAll('button');
      for (var i = 0; i < buttons.length; i++) buttons[i].disabled = true;
      if (s.ws && s.ws.readyState === 1) {
        s.ws.send(JSON.stringify({
          type: 'ask',
          action: action || 'accept',
          answers: answers || []
        }));
      }
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
          if (s.id && s.cwd) store('pane-last-sid:' + s.cwd, s.id);
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
        case 'tool':
          renderTool(s, msg);
          break;
        case 'err':
          s.addErr(msg.text || 'error');
          s.setChrome(msg.text || 'error', 'err');
          break;
        case 'busy':
          s.busy = true;
          s.asking = false;
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
      if (cwd && s.cwd && s.cwd !== cwd) continue;
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
      if (opts.resume) newSession(cwd);
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
    fetch(url)
      .then(function (r) { return r.ok ? r.json() : []; })
      .then(function (list) {
        diskSessions = list || [];
        sessPaintKey = '';
        applyGrokTitles(diskSessions);
        paintSessions();
        if (opts.resume) resumeLatest(cwd, diskSessions);
        loadProjects();
      })
      .catch(function () {
        diskSessions = [];
        sessPaintKey = '';
        paintSessions();
        if (opts.resume) resumeLatest(cwd, []);
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
    fetch(paneHTTP() + '/v1/projects?cwd=' + encodeURIComponent(cwd), {
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

  function loadProjects() {
    if (!projectsEl) return;
    fetch(paneHTTP() + '/v1/projects')
      .then(function (r) { return r.ok ? r.json() : []; })
      .then(function (list) {
        list = list || [];
        list.forEach(function (p) {
          if (p && p.cwd && p.name) projectNames[normPath(p.cwd)] = p.name;
        });
        if (project) {
          var seen = list.some(function (p) { return p && samePath(p.cwd, project); });
          if (!seen) {
            list = [{ cwd: project, name: projectLabel(project), sessions: 0 }].concat(list);
          }
        }
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
            inp.addEventListener('keydown', function (e) {
              if (e.key === 'Enter') {
                e.preventDefault();
                commitProjectRename(p.cwd, inp.value);
              }
              if (e.key === 'Escape') {
                e.preventDefault();
                renamingProject = null;
                loadProjects();
              }
              e.stopPropagation();
            });
            inp.addEventListener('blur', function () { commitProjectRename(p.cwd, inp.value); });
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
      })
      .catch(function () {});
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

  function resumeLatest(cwd, list) {
    var firstId = list && list[0] && list[0].id;
    if (firstId) {
      var existing = sessions.filter(function (s) {
        return !s.dead && (s.id === firstId || s.resumeID === firstId);
      })[0];
      if (existing) {
        activate(existing);
        return;
      }
      newSession(cwd, resumeOpts(list[0]));
      return;
    }
    var open = sessions.filter(function (s) { return !s.dead && s.cwd === cwd; })[0];
    if (open) {
      activate(open);
      return;
    }
    newSession(cwd);
  }

  function loadRemote() {
    if (!remoteEl) return;
    fetch(paneHTTP() + '/v1/remote-sessions')
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
    if (s === active) active = null;
    s.shutdown();
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
      fetch(paneHTTP() + '/v1/projects?cwd=' + encodeURIComponent(p.cwd), { method: 'DELETE' })
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
              fetch(paneHTTP() + '/v1/projects')
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
    var left = size > used ? size - used : 0;
    var model = (active && active.model) || u.model || '—';
    var effort = (active && active.effort) || '—';
    var tools = (u.tools && u.tools.length) ? u.tools.join(', ') : '—';
    var turns = u.turns != null ? u.turns : '—';
    var calls = u.toolCalls != null ? u.toolCalls : '—';
    var dur = u.duration ? fmtDur(u.duration) : '—';
    usagePop.innerHTML = '';
    var h = document.createElement('h3');
    h.textContent = 'Context';
    usagePop.appendChild(h);
    var big = document.createElement('p');
    big.className = 'u-big';
    big.textContent = size ? (fmtNum(used) + ' / ' + fmtNum(size) + ' tokens') : 'No usage yet';
    usagePop.appendChild(big);
    var sub = document.createElement('p');
    sub.className = 'u-sub';
    sub.textContent = size ? (pct + '% used · ' + fmtNum(left) + ' left') : 'Send a message to start counting.';
    usagePop.appendChild(sub);
    var dl = document.createElement('dl');
    function row(k, v) {
      var dt = document.createElement('dt');
      dt.textContent = k;
      var dd = document.createElement('dd');
      dd.textContent = v;
      dl.appendChild(dt);
      dl.appendChild(dd);
    }
    row('Model', model);
    row('Effort', shortEffort(effort));
    row('Turns', String(turns));
    row('Tools', String(calls) + (tools !== '—' ? ' · ' + tools : ''));
    row('Session', dur);
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
    fetch(paneHTTP() + '/v1/usage?cwd=' + encodeURIComponent(s.cwd) + '&id=' + encodeURIComponent(s.id))
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

  function send() {
    if (!active || active.dead || !active.ws || active.ws.readyState !== 1) return;
    var item = snapshotComposer();
    if (!item.text && !(item.files && item.files.length)) return;
    if (active.busy) {
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
      return;
    }
    dispatch(active, item);
  }

  sendBtn.addEventListener('click', send);
  input.addEventListener('input', function () { grow(); syncSend(); });
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
    if (e.key !== 'Escape' || !active) return;
    if (active.asking && active.askEl) {
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
      active.queue.pop();
      paintQueue();
      paintSessions();
    }
  });

  function dropPending(i) {
    var f = pending[i];
    if (!f) return;
    pending.splice(i, 1);
    if (f.preview) {
      try { URL.revokeObjectURL(f.preview); } catch (e) {}
    }
    if (f.copied && f.path) {
      var cwd = (active && active.cwd) || project;
      if (cwd) {
        fetch(paneHTTP() + '/v1/upload?cwd=' + encodeURIComponent(cwd) + '&path=' + encodeURIComponent(f.path), { method: 'DELETE' }).catch(function () {});
      }
    }
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
      fetch(paneHTTP() + '/v1/upload?cwd=' + encodeURIComponent(cwd), { method: 'POST', body: fd })
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
      fetch(paneHTTP() + '/v1/upload?cwd=' + encodeURIComponent(cwd), {
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

  function setProject(path) {
    if (!path) return;
    path = normPath(path);
    project = path;
    projectBtn.textContent = projectLabel(path);
    projectBtn.title = path + ' — click to copy';
    try { localStorage.setItem('pane-project', path); } catch (e) {}
    loadProjects();
    if (active && active.cwd === path) {
      paintCwd(path);
      loadHistory(path);
      return;
    }
    loadHistory(path, { resume: true, prune: true });
  }

  function openProject() {
    if (railLocked() || pickingProject) return;
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

  document.addEventListener('click', function (e) {
    var t = e.target;
    var a = t && t.closest ? t.closest('a[href]') : null;
    if (!a) return;
    var href = a.getAttribute('href') || '';
    if (!/^(https?:|mailto:)/i.test(href)) return;
    e.preventDefault();
    e.stopPropagation();
    openExternal(href);
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
      fetch(paneHTTP() + '/meta')
        .then(function (r) { return r.json(); })
        .then(function (meta) {
          if (!saved) saved = meta.cwd || '';
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
    var api = desktopAPI();
    if (api && typeof api.PaneOrigin === 'function') {
      Promise.resolve(api.PaneOrigin()).then(function (o) {
        if (o) {
          window.__paneOrigin = o;
          try { localStorage.setItem('pane-url', o); } catch (e) {}
        }
        start();
      }).catch(function () { start(); });
      return;
    }
    start();
  }
  boot();
})();
