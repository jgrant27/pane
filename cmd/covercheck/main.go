// Covercheck fails if a go cover profile is under -min percent.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func coverage(path string) (float64, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, 0, fmt.Errorf("empty cover profile")
	}
	if !strings.HasPrefix(sc.Text(), "mode:") {
		return 0, 0, 0, fmt.Errorf("bad cover profile: %s", sc.Text())
	}
	stmts, hit := 0, 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return 0, 0, 0, fmt.Errorf("bad cover line: %s", line)
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, 0, 0, fmt.Errorf("statements: %w", err)
		}
		c, err := strconv.Atoi(fields[2])
		if err != nil {
			return 0, 0, 0, fmt.Errorf("count: %w", err)
		}
		stmts += n
		if c > 0 {
			hit += n
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, 0, err
	}
	if stmts == 0 {
		return 100, 0, 0, nil
	}
	return 100 * float64(hit) / float64(stmts), hit, stmts, nil
}

func check(min float64, path string) error {
	pct, hit, stmts, err := coverage(path)
	if err != nil {
		return err
	}
	fmt.Printf("coverage %.1f%% (%d/%d statements, min %.0f%%)\n", pct, hit, stmts, min)
	if pct+1e-9 < min {
		return fmt.Errorf("coverage %.1f%% is below %.0f%%", pct, min)
	}
	return nil
}

func main() {
	min := flag.Float64("min", 90, "minimum coverage percent")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: covercheck -min=90 cover.out\n")
		os.Exit(2)
	}
	if err := check(*min, flag.Arg(0)); err != nil {
		fmt.Fprintf(os.Stderr, "covercheck: %v\n", err)
		os.Exit(1)
	}
}
