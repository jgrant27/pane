package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type remoteSession struct {
	sessionInfo
	Host   string `json:"host"`
	Origin string `json:"origin"`
}

type tsPeer struct {
	Host   string
	DNS    string
	Online bool
	Self   bool
}

var remoteURLScheme = "https"

func handleRemoteSessions(w http.ResponseWriter, r *http.Request) {
	list := cachedRemoteSessions()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

var (
	remoteMu         sync.Mutex
	remoteAt         time.Time
	remoteList       []remoteSession
	remoteRefreshing bool
)

func cachedRemoteSessions() []remoteSession {
	const ttl = 30 * time.Second
	remoteMu.Lock()
	list := remoteList
	age := time.Since(remoteAt)
	needRefresh := list == nil || age >= ttl/2
	if needRefresh && !remoteRefreshing {
		remoteRefreshing = true
		go func() {
			next := discoverRemoteSessions(2500 * time.Millisecond)
			if next == nil {
				next = []remoteSession{}
			}
			remoteMu.Lock()
			remoteList = next
			remoteAt = time.Now()
			remoteRefreshing = false
			remoteMu.Unlock()
		}()
	}
	remoteMu.Unlock()
	if list != nil {
		return list
	}
	next := discoverRemoteSessions(2500 * time.Millisecond)
	if next == nil {
		next = []remoteSession{}
	}
	remoteMu.Lock()
	if remoteList == nil {
		remoteList = next
		remoteAt = time.Now()
	} else {
		next = remoteList
	}
	remoteMu.Unlock()
	return next
}

func discoverRemoteSessions(budget time.Duration) []remoteSession {
	out := []remoteSession{}
	peers := tailscalePeerList()
	if len(peers) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, p := range peers {
		if p.Self || !p.Online || p.DNS == "" {
			continue
		}
		wg.Add(1)
		go func(p tsPeer) {
			defer wg.Done()
			origin := remoteURLScheme + "://" + strings.TrimSuffix(p.DNS, ".")
			if !remotePaneOK(ctx, origin) {
				return
			}
			found := fetchRemoteSessions(ctx, origin)
			host := p.Host
			if host == "" {
				host = strings.Split(strings.TrimSuffix(p.DNS, "."), ".")[0]
			}
			mu.Lock()
			for _, s := range found {
				s.Host = host
				s.Origin = origin
				out = append(out, s)
			}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool {
		return out[i].Updated > out[j].Updated
	})
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func tailscalePeerList() []tsPeer {
	if _, err := lookPath("tailscale"); err != nil {
		return nil
	}
	out, err := tailscaleJSON()
	if err != nil {
		return nil
	}
	var raw struct {
		Self struct {
			DNSName  string `json:"DNSName"`
			HostName string `json:"HostName"`
		} `json:"Self"`
		Peer map[string]struct {
			DNSName  string `json:"DNSName"`
			HostName string `json:"HostName"`
			Online   bool   `json:"Online"`
		} `json:"Peer"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	selfDNS := strings.TrimSuffix(raw.Self.DNSName, ".")
	list := []tsPeer{{
		Host:   raw.Self.HostName,
		DNS:    raw.Self.DNSName,
		Online: true,
		Self:   true,
	}}
	for _, p := range raw.Peer {
		list = append(list, tsPeer{
			Host:   p.HostName,
			DNS:    p.DNSName,
			Online: p.Online,
			Self:   strings.TrimSuffix(p.DNSName, ".") == selfDNS,
		})
	}
	return list
}

func remotePaneOK(ctx context.Context, origin string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/meta", nil)
	if err != nil {
		return false
	}
	res, err := remoteHTTP().Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return false
	}
	var meta struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(res.Body).Decode(&meta) != nil {
		return false
	}
	return meta.Name == "Grok Pane"
}

func fetchRemoteSessions(ctx context.Context, origin string) []remoteSession {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/v1/sessions", nil)
	if err != nil {
		return nil
	}
	res, err := remoteHTTP().Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil
	}
	var list []sessionInfo
	if json.NewDecoder(res.Body).Decode(&list) != nil {
		return nil
	}
	out := make([]remoteSession, 0, len(list))
	for _, s := range list {
		if s.ID == "" {
			continue
		}
		out = append(out, remoteSession{sessionInfo: s})
	}
	return out
}

func remoteHTTP() *http.Client {
	return &http.Client{Timeout: 1500 * time.Millisecond}
}
