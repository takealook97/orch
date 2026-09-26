package main

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var criterionLine = regexp.MustCompile(`^\s*(\d+)\.\s+(.+?)\s*$`)

// acceptanceItems reads the numbered criteria of acceptance.md.
func acceptanceItems(r *Run) ([][2]string, error) {
	f, err := os.Open(filepath.Join(r.dir(), "acceptance.md"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var items [][2]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := criterionLine.FindStringSubmatch(sc.Text()); m != nil {
			items = append(items, [2]string{m[1], m[2]})
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("acceptance.md has no numbered acceptance criteria")
	}
	return items, nil
}

// checklist renders the fixed neutral-lane table (design §10.3). With shuffle the order is
// permuted (numbers kept) so the Head can compare two passes for position bias.
func checklist(r *Run, shuffle bool, seed int64) (string, error) {
	items, err := acceptanceItems(r)
	if err != nil {
		return "", err
	}
	if shuffle {
		if seed == 0 {
			seed = time.Now().UnixNano()
		}
		rng := rand.New(rand.NewSource(seed))
		rng.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	}
	var b strings.Builder
	b.WriteString("| # | acceptance criteria | judgment | evidence location | notes |\n|---|---|---|---|---|\n")
	for _, it := range items {
		fmt.Fprintf(&b, "| %s | %s | met / unmet / cannot verify from evidence | | |\n", it[0], strings.ReplaceAll(it[1], "|", `\|`))
	}
	b.WriteString("\n## additions outside the request\n- \n## points that may confuse the user\n- \n")
	b.WriteString("\nRule: one judgment per item and evidence for every judgment. Copy machine-checkable results from the mechanical lane.\n")
	if shuffle {
		fmt.Fprintf(&b, "(shuffled order seed=%d. Head directly checks items that differ from the first judgment)\n", seed)
	}
	return b.String(), nil
}
