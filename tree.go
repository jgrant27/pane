package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type treeEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Path string `json:"path"`
}

func handleTree(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		http.Error(w, "not a directory", http.StatusNotFound)
		return
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	out := make([]treeEntry, 0, len(ents))
	for _, e := range ents {
		name := e.Name()
		if name == ".DS_Store" {
			continue
		}
		out = append(out, treeEntry{
			Name: name,
			Dir:  e.IsDir(),
			Path: filepath.Join(abs, name),
		})
		if len(out) >= 400 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
