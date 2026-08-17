# Pane

A local web window onto [`grok agent serve`](https://grok.com).

Grok Build talks ACP on a websocket. Pane is a small Go process that starts that server if needed, proxies the socket, and gives you a browser transcript. The agent still runs on **this machine**. Pane is a face, not a second computer.

Light theme is the default. Dark is a toggle.

## Run

You need Go, `grok` on `PATH`, and a working Grok login (`grok` TUI or `XAI_API_KEY`).

```bash
git clone https://github.com/jgrant27/pane
cd pane
make
./pane
./pane -cwd ~/src/my-project
```

That starts `grok agent serve` if nothing is on `:2419`, serves the UI on `http://127.0.0.1:7420`, and opens the browser. Type in the box, Enter to send, Shift+Enter for a newline, Escape to cancel the turn.

The agent secret is `-secret`, `$GROK_AGENT_SECRET`, `$PANE_SECRET`, or `~/.grok/pane.secret` (created on first run). Ctrl-C stops Pane and anything **it** started. An agent that was already running is left alone.

```bash
make install                 # $(PREFIX)/bin, default ~/.local/bin
make run ARGS='-cwd .'
```

## Tailscale

```bash
./pane -tailscale
```

That runs `tailscale serve` in front and requires `Tailscale-User-Login`. Direct hits on `:7420` get 403.

**Do not use `tailscale funnel`.** Funnel publishes the page — and the agent behind it — to the public internet.

## What it does

- Auto-approves tool permission prompts (`yoloMode`). Treat it like a local shell with a browser attached.
- Does not implement the agent's filesystem or terminal ACP callbacks. Tools run inside `grok agent serve` on this host.
- Thoughts are off until you flip the header toggle. The status line says `thinking…` so a quiet turn does not look dead.

## Flags

| Flag | Default | |
| --- | --- | --- |
| `-listen` | `127.0.0.1:7420` | HTTP |
| `-agent` | `ws://127.0.0.1:2419` | `grok agent serve` base |
| `-agent-bind` | `127.0.0.1:2419` | where to start the agent |
| `-secret` | env / `~/.grok/pane.secret` | `server-key` |
| `-cwd` | `$HOME` | ACP working directory |
| `-tailscale` | false | `tailscale serve` + identity gate |
| `-no-agent` | false | do not start the agent |
| `-no-open` | false | do not open a browser |

## Layout

```
pane/
  main.go      CLI, HTTP, agent, Tailscale
  proxy.go     browser WS ↔ ACP
  web/         page + vendored xterm.js
```

## License

MIT. xterm.js in `web/vendor/` is MIT (xterm.js authors).
