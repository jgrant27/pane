// Bump prints the next pane version and optionally writes it into the
// repo files GitHub Release / the desktop bundle read.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type ver struct {
	major, minor, patch int
}

func (v ver) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }
func (v ver) Tag() string    { return "v" + v.String() }

func (v ver) less(o ver) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

func (v ver) bump(kind string) ver {
	switch kind {
	case "major":
		return ver{v.major + 1, 0, 0}
	case "minor":
		return ver{v.major, v.minor + 1, 0}
	default:
		return ver{v.major, v.minor, v.patch + 1}
	}
}

func parseVer(s string) (ver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return ver{}, false
	}
	a, err1 := strconv.Atoi(parts[0])
	b, err2 := strconv.Atoi(parts[1])
	c, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || a < 0 || b < 0 || c < 0 {
		return ver{}, false
	}
	return ver{a, b, c}, true
}

func maxVer(list []ver) ver {
	var best ver
	found := false
	for _, v := range list {
		if !found || best.less(v) {
			best = v
			found = true
		}
	}
	return best
}

func gitTags() []ver {
	out, err := exec.Command("git", "tag", "-l", "v*").Output()
	if err != nil {
		return nil
	}
	var vs []ver
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := parseVer(line); ok {
			vs = append(vs, v)
		}
	}
	return vs
}

func fileVer(path string, re *regexp.Regexp) []ver {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var vs []ver
	for _, m := range re.FindAllSubmatch(b, -1) {
		for i := 1; i < len(m); i++ {
			if v, ok := parseVer(string(m[i])); ok {
				vs = append(vs, v)
				break
			}
		}
	}
	return vs
}

var (
	reSemver     = regexp.MustCompile(`(?m)^(v?\d+\.\d+\.\d+)\s*$`)
	reWails      = regexp.MustCompile(`("productVersion"\s*:\s*")(\d+\.\d+\.\d+)(")`)
	rePlistShort = regexp.MustCompile(`(<key>CFBundleShortVersionString</key>\s*<string>)(\d+\.\d+\.\d+)(</string>)`)
	rePlistBuild = regexp.MustCompile(`(<key>CFBundleVersion</key>\s*<string>)(\d+\.\d+\.\d+)(</string>)`)
	reProxy      = regexp.MustCompile(`("name":\s*"grok-pane",\s*"title":\s*"Grok Pane",\s*"version":\s*")(\d+\.\d+\.\d+)(")`)
)

func current(root string) ver {
	var vs []ver
	vs = append(vs, gitTags()...)
	vs = append(vs, fileVer(filepath.Join(root, "VERSION"), reSemver)...)
	vs = append(vs, fileVer(filepath.Join(root, "desktop", "wails.json"), reWails)...)
	vs = append(vs, fileVer(filepath.Join(root, "desktop", "Info.plist"), rePlistShort)...)
	return maxVer(vs)
}

func replaceAll(path string, re *regexp.Regexp, next string, sub int) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out := re.ReplaceAllFunc(b, func(m []byte) []byte {
		parts := re.FindSubmatch(m)
		if len(parts) <= sub {
			return m
		}
		var buf []byte
		for i := 1; i < len(parts); i++ {
			if i == sub {
				buf = append(buf, next...)
			} else {
				buf = append(buf, parts[i]...)
			}
		}
		return buf
	})
	if bytes.Equal(b, out) {
		return nil
	}
	return os.WriteFile(path, out, 0o644)
}

func writeVersion(root, next string) error {
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte(next+"\n"), 0o644); err != nil {
		return err
	}
	if err := replaceAll(filepath.Join(root, "desktop", "wails.json"), reWails, next, 2); err != nil {
		return err
	}
	plist := filepath.Join(root, "desktop", "Info.plist")
	if err := replaceAll(plist, rePlistShort, next, 2); err != nil {
		return err
	}
	if err := replaceAll(plist, rePlistBuild, next, 2); err != nil {
		return err
	}
	return replaceAll(filepath.Join(root, "proxy.go"), reProxy, next, 2)
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		wd, werr := os.Getwd()
		if werr != nil {
			return "", err
		}
		return wd, nil
	}
	return strings.TrimSpace(string(out)), nil
}

func doBump(kind string, write bool) (string, error) {
	switch kind {
	case "patch", "minor", "major":
	default:
		return "", fmt.Errorf("unknown bump %q", kind)
	}
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	next := current(root).bump(kind)
	if write {
		if err := writeVersion(root, next.String()); err != nil {
			return "", err
		}
	}
	return next.Tag(), nil
}

func main() {
	kind := flag.String("bump", "patch", "patch, minor, or major")
	write := flag.Bool("write", false, "update VERSION and stamped files")
	flag.Parse()
	tag, err := doBump(*kind, *write)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pane: %v\n", err)
		if strings.Contains(err.Error(), "unknown bump") {
			os.Exit(2)
		}
		os.Exit(1)
	}
	fmt.Print(tag)
}
