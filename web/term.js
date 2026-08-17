/* xterm = transcript. Input is the footer. ACP lives in Go. */
(function () {
  var themes = {
    light: {
      background: '#f4f1ea',
      foreground: '#1c1914',
      cursor: '#f4f1ea',
      cursorAccent: '#f4f1ea',
      selectionBackground: '#d4cfc3'
    },
    dark: {
      background: '#111111',
      foreground: '#e8e4d9',
      cursor: '#111111',
      cursorAccent: '#111111',
      selectionBackground: '#3a3428'
    }
  };

  function currentTheme() {
    return document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light';
  }

  var term = new Terminal({
    cursorBlink: false,
    disableStdin: true,
    fontSize: 14,
    fontFamily: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace',
    theme: themes[currentTheme()],
    scrollback: 8000
  });
  var fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(document.getElementById('term'));
  fit.fit();
  window.addEventListener('resize', function () { fit.fit(); });

  var themeBtn = document.getElementById('theme');
  function paintTheme() {
    var name = currentTheme();
    themeBtn.textContent = name === 'dark' ? 'Light' : 'Dark';
    term.options.theme = themes[name];
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

  var showThoughts = false;
  var thoughtsBtn = document.getElementById('thoughts');
  thoughtsBtn.addEventListener('click', function () {
    showThoughts = !showThoughts;
    thoughtsBtn.setAttribute('aria-pressed', showThoughts ? 'true' : 'false');
  });

  var status = document.getElementById('status');
  var cwdEl = document.getElementById('cwd');
  var input = document.getElementById('in');
  var sendBtn = document.getElementById('send');
  var busy = true;
  var startedReply = false;
  var tools = {};
  var history = [];
  var histAt = -1;

  function setStatus(text, cls) {
    status.textContent = text;
    status.className = cls || '';
  }

  function setBusy(on) {
    busy = on;
    sendBtn.disabled = on || !ws || ws.readyState !== 1;
    if (!on) input.focus();
  }

  function dim(s) { return '\x1b[38;5;245m' + s + '\x1b[0m'; }

  function send() {
    var text = input.value.replace(/\s+$/, '');
    if (!text || busy || !ws || ws.readyState !== 1) return;
    input.value = '';
    histAt = -1;
    if (history[history.length - 1] !== text) history.push(text);
    term.writeln('');
    term.writeln(dim('you') + '  ' + text.replace(/\n/g, '\r\n    '));
    term.writeln('');
    startedReply = false;
    setBusy(true);
    setStatus('thinking…', 'busy');
    ws.send(JSON.stringify({ type: 'in', text: text }));
  }

  sendBtn.addEventListener('click', send);
  input.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
      return;
    }
    if (e.key === 'Escape' && busy) {
      ws.send(JSON.stringify({ type: 'cancel' }));
      return;
    }
    if (e.key === 'ArrowUp' && !e.shiftKey && input.selectionStart === 0) {
      if (!history.length) return;
      if (histAt < 0) histAt = history.length;
      if (histAt > 0) histAt--;
      input.value = history[histAt] || '';
      e.preventDefault();
    }
    if (e.key === 'ArrowDown' && !e.shiftKey) {
      if (histAt < 0) return;
      histAt++;
      if (histAt >= history.length) {
        histAt = -1;
        input.value = '';
      } else {
        input.value = history[histAt];
      }
      e.preventDefault();
    }
  });

  var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  var ws = null;
  var reconnects = 0;

  function connect() {
    setStatus(reconnects ? 'reconnecting…' : 'connecting…');
    ws = new WebSocket(proto + '//' + location.host + '/ws');
    ws.onopen = function () { setStatus('handshaking…'); };
    ws.onerror = function () { setStatus('socket error', 'err'); };
    ws.onclose = function () {
      setBusy(true);
      setStatus('disconnected', 'err');
      reconnects++;
      var wait = Math.min(8000, 400 * reconnects);
      setTimeout(connect, wait);
    };
    ws.onmessage = function (ev) {
      var msg;
      try { msg = JSON.parse(ev.data); } catch (e) { return; }
      switch (msg.type) {
        case 'ready':
          reconnects = 0;
          cwdEl.textContent = msg.cwd || '';
          cwdEl.title = msg.cwd || '';
          setStatus('ready', 'ok');
          setBusy(false);
          break;
        case 'out':
          if (!startedReply) startedReply = true;
          if (msg.text) term.write(String(msg.text).replace(/\n/g, '\r\n'));
          break;
        case 'thought':
          if (showThoughts && msg.text) {
            term.write('\x1b[38;5;240m' + String(msg.text).replace(/\n/g, '\r\n') + '\x1b[0m');
          }
          break;
        case 'tool':
          renderTool(msg);
          break;
        case 'err':
          term.writeln('');
          term.writeln('\x1b[31m' + (msg.text || 'error') + '\x1b[0m');
          setStatus(msg.text || 'error', 'err');
          break;
        case 'busy':
          setBusy(true);
          setStatus('thinking…', 'busy');
          startedReply = false;
          tools = {};
          break;
        case 'idle':
          if (startedReply) term.writeln('');
          setStatus('ready', 'ok');
          setBusy(false);
          break;
      }
    };
  }
  connect();

  function renderTool(msg) {
    var id = msg.id || msg.text || 'tool';
    var prev = tools[id];
    var status = msg.status || '';
    var title = msg.text || 'tool';
    if (!prev) {
      tools[id] = { title: title, status: status };
      term.writeln(dim('· ' + title));
      return;
    }
    if (status && status !== prev.status && (status === 'completed' || status === 'failed' || status === 'cancelled')) {
      prev.status = status;
    }
  }
})();
