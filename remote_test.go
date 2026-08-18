package main

import (
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
