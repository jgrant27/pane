package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxUpload = 20 << 20

type uploadInfo struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Mime   string `json:"mime"`
	Size   int64  `json:"size"`
	Copied bool   `json:"copied"`
}

func sanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\x00", "")
	if name == "" || name == "." || name == ".." {
		return "upload"
	}
	return name
}

func uniquePath(dir, name string) string {
	base := sanitizeName(name)
	dest := filepath.Join(dir, base)
	if _, err := os.Stat(dest); err != nil {
		return dest
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 2; i < 1000; i++ {
		dest = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		if _, err := os.Stat(dest); err != nil {
			return dest
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), ext))
}

func detectMIME(name string, head []byte) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	if len(head) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(head)
}

func underCwd(cwd, path string) bool {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absCwd, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, io.LimitReader(in, maxUpload+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(dest)
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	return nil
}

func attachPath(cwd, src string) (uploadInfo, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return uploadInfo{}, fmt.Errorf("path required")
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return uploadInfo{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return uploadInfo{}, err
	}
	if st.IsDir() {
		return uploadInfo{}, fmt.Errorf("not a file")
	}
	if st.Size() > maxUpload {
		return uploadInfo{}, fmt.Errorf("file too large (20MB)")
	}
	dest := abs
	copied := false
	if !underCwd(cwd, abs) {
		dest = uniquePath(cwd, filepath.Base(abs))
		if err := copyFile(abs, dest); err != nil {
			return uploadInfo{}, err
		}
		st, err = os.Stat(dest)
		if err != nil {
			return uploadInfo{}, err
		}
		copied = true
	}
	head := make([]byte, 512)
	if rf, err := os.Open(dest); err == nil {
		n, _ := rf.Read(head)
		head = head[:n]
		_ = rf.Close()
	}
	return uploadInfo{
		Name:   filepath.Base(dest),
		Path:   dest,
		Mime:   detectMIME(dest, head),
		Size:   st.Size(),
		Copied: copied,
	}, nil
}

func writeUploadJSON(w http.ResponseWriter, info uploadInfo) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
		path := strings.TrimSpace(r.URL.Query().Get("path"))
		if cwd == "" || path == "" {
			http.Error(w, "cwd and path required", http.StatusBadRequest)
			return
		}
		abs, err := filepath.Abs(cwd)
		if err != nil {
			http.Error(w, "cwd", http.StatusBadRequest)
			return
		}
		if !underCwd(abs, path) {
			http.Error(w, "not in project", http.StatusBadRequest)
			return
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if cwd == "" {
		http.Error(w, "cwd required", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		http.Error(w, "cwd", http.StatusBadRequest)
		return
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		http.Error(w, "cwd not a directory", http.StatusBadRequest)
		return
	}
	ct := r.Header.Get("Content-Type")
	qPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if qPath != "" || strings.Contains(ct, "application/json") {
		src := qPath
		if src == "" {
			var body struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
				http.Error(w, "path required", http.StatusBadRequest)
				return
			}
			src = body.Path
		}
		info, err := attachPath(abs, src)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeUploadJSON(w, info)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+1<<20)
	if err := r.ParseMultipartForm(maxUpload + 1<<20); err != nil {
		http.Error(w, "file too large (20MB)", http.StatusRequestEntityTooLarge)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer f.Close()
	if hdr.Size > maxUpload {
		http.Error(w, "file too large (20MB)", http.StatusRequestEntityTooLarge)
		return
	}
	dest := uniquePath(abs, hdr.Filename)
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		http.Error(w, "could not write file", http.StatusInternalServerError)
		return
	}
	n, copyErr := io.Copy(out, io.LimitReader(f, maxUpload+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil || n > maxUpload {
		_ = os.Remove(dest)
		http.Error(w, "could not write file", http.StatusInternalServerError)
		return
	}
	head := make([]byte, 512)
	if rf, err := os.Open(dest); err == nil {
		n, _ := rf.Read(head)
		head = head[:n]
		_ = rf.Close()
	}
	writeUploadJSON(w, uploadInfo{
		Name:   filepath.Base(dest),
		Path:   dest,
		Mime:   detectMIME(dest, head),
		Size:   n,
		Copied: true,
	})
}
