package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCachedRemoteSessionsFastPath(t *testing.T) {
	remoteMu.Lock()
	prevList, prevAt, prevRef := remoteList, remoteAt, remoteRefreshing
	remoteList = []remoteSession{{Host: "box"}}
	remoteAt = time.Now()
	remoteRefreshing = true
	remoteMu.Unlock()
	t.Cleanup(func() {
		remoteMu.Lock()
		remoteList, remoteAt, remoteRefreshing = prevList, prevAt, prevRef
		remoteMu.Unlock()
	})

	start := time.Now()
	got := cachedRemoteSessions()
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("cache hit took %s", time.Since(start))
	}
	if len(got) != 1 || got[0].Host != "box" {
		t.Fatalf("%+v", got)
	}
}

func TestRemotePaneFetchAndDiscover(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		switch r.URL.Path {
		case "/meta":
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "Grok Pane"})
		case "/v1/sessions":
			_ = json.NewEncoder(w).Encode([]sessionInfo{
				{ID: "01aaaaaaaaaaaaaaaaaaaaaaaa", Title: "A", Updated: "2026-08-17T12:00:00Z"},
				{ID: "", Title: "skip"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	ctx := context.Background()
	if !remotePaneOK(ctx, srv.URL) {
		t.Fatal("meta")
	}
	if remotePaneOK(ctx, srv.URL+"/nope") {
		t.Fatal("bad meta path should fail")
	}
	found := fetchRemoteSessions(ctx, srv.URL)
	if len(found) != 1 || found[0].ID == "" {
		t.Fatalf("%+v", found)
	}
	oldScheme := remoteURLScheme
	oldPath, oldJSON := lookPath, tailscaleJSON
	remoteURLScheme = "http"
	lookPath = func(string) (string, error) { return "/usr/bin/tailscale", nil }
	tailscaleJSON = func() ([]byte, error) {
		return []byte(`{
			"Self":{"DNSName":"self.ts.net.","HostName":"self"},
			"Peer":{
				"k":{"DNSName":"` + host + `","HostName":"peer","Online":true},
				"off":{"DNSName":"off.ts.net.","HostName":"off","Online":false}
			}
		}`), nil
	}
	t.Cleanup(func() {
		remoteURLScheme = oldScheme
		lookPath, tailscaleJSON = oldPath, oldJSON
	})
	peers := tailscalePeerList()
	if len(peers) < 2 {
		t.Fatalf("peers %+v", peers)
	}
	got := discoverRemoteSessions(2 * time.Second)
	if len(got) != 1 || got[0].Host != "peer" || got[0].Origin == "" {
		t.Fatalf("%+v", got)
	}
	if remoteHTTP() == nil {
		t.Fatal("client")
	}
}

func TestCachedRemoteSessionsCold(t *testing.T) {
	remoteMu.Lock()
	prevList, prevAt, prevRef := remoteList, remoteAt, remoteRefreshing
	remoteList = nil
	remoteAt = time.Time{}
	remoteRefreshing = false
	remoteMu.Unlock()
	t.Cleanup(func() {
		remoteMu.Lock()
		remoteList, remoteAt, remoteRefreshing = prevList, prevAt, prevRef
		remoteMu.Unlock()
	})
	oldPath := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	// The hook has to outlive the refresh goroutine that reads it, or the
	// restore below races it — which is what go test -race reported.
	t.Cleanup(func() { lookPath = oldPath })
	t.Cleanup(waitRemoteRefresh)
	got := cachedRemoteSessions()
	if got == nil {
		t.Fatal("nil")
	}
}

func TestWaitRemoteRefreshJoinsBackgroundRefresh(t *testing.T) {
	waitRemoteRefresh()
	remoteMu.Lock()
	prevList, prevAt, prevRef := remoteList, remoteAt, remoteRefreshing
	// A cached-but-stale list is what sends the refresh to the background.
	remoteList = []remoteSession{}
	remoteAt = time.Now().Add(-time.Minute)
	remoteRefreshing = false
	remoteMu.Unlock()

	blocked := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(blocked) }) }
	oldPath, oldJSON := lookPath, tailscaleJSON
	lookPath = func(string) (string, error) { return "/usr/bin/tailscale", nil }
	tailscaleJSON = func() ([]byte, error) {
		<-blocked
		return []byte(`{"Self":{"DNSName":"self.ts.net.","HostName":"self"}}`), nil
	}
	// Cleanups run last-registered-first: unblock the refresh, join it, then
	// put the hooks and the cache back.
	t.Cleanup(func() {
		remoteMu.Lock()
		remoteList, remoteAt, remoteRefreshing = prevList, prevAt, prevRef
		remoteMu.Unlock()
	})
	t.Cleanup(func() { lookPath, tailscaleJSON = oldPath, oldJSON })
	t.Cleanup(waitRemoteRefresh)
	t.Cleanup(release)

	cachedRemoteSessions()
	remoteMu.Lock()
	stillStale := remoteAt.Before(time.Now().Add(-30 * time.Second))
	remoteMu.Unlock()
	if !stillStale {
		t.Fatal("refresh should still be in flight")
	}
	release()
	waitRemoteRefresh()
	remoteMu.Lock()
	fresh := time.Since(remoteAt) < time.Minute
	running := remoteRefreshing
	remoteMu.Unlock()
	if !fresh || running {
		t.Fatalf("waitRemoteRefresh returned before the refresh finished (fresh=%v running=%v)", fresh, running)
	}
}
