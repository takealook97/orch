package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Plan every change before touching files. Ambiguous ownership is preserved.
func uninstall(repo string, apply bool) (*installReport, error) {
	rep := &installReport{}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return rep, err
	}
	manifest, err := loadOwnership()
	if err != nil {
		return rep, err
	}
	if manifest != nil && manifest.Repo != repo {
		return rep, fmt.Errorf("installation belongs to %s; specify that --repo", manifest.Repo)
	}
	sharedBin := filepath.Join(home(), ".local/bin/orch")
	sharedTarget, _ := os.Readlink(sharedBin)
	ownShared := manifest != nil || sharedTarget == filepath.Join(repo, "bin/orch")
	wantBlock := constitutionBlock
	if manifest != nil {
		wantBlock = manifest.Block
	}
	type change struct {
		path          string
		before, after []byte
		link          string
		remove        bool
	}
	var changes []change
	owned := func(command any) bool {
		c, ok := command.(string)
		if !ok {
			return false
		}
		bins := []string{filepath.Join(repo, "bin/orch")}
		if ownShared {
			bins = append(bins, sharedBin)
		}
		if manifest != nil {
			bins = append(bins, manifest.Bin)
		}
		for _, bin := range bins {
			for _, cli := range []string{"claude", "claude-prompt", "codex", "codex-prompt", "agy"} {
				if c == hookCommand(bin, cli) {
					return true
				}
			}
		}
		return false
	}
	for _, p := range constitutionFiles() {
		if info, e := os.Lstat(p); e == nil && !info.Mode().IsRegular() {
			rep.add("preserve non-regular config: %s", p)
			continue
		}
		b, e := os.ReadFile(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return rep, e
		}
		s := string(b)
		beg, end := strings.Count(s, "<!-- orch:begin"), strings.Count(s, markEnd)
		if beg == 0 && end == 0 {
			continue
		}
		block, ok := extractBlock(s)
		if beg != 1 || end != 1 || !ok {
			return rep, fmt.Errorf("ambiguous constitution markers: %s; unchanged", p)
		}
		if block != strings.TrimSpace(wantBlock) {
			rep.add("preserve modified block: %s", p)
			continue
		}
		i, j := strings.Index(s, markBegin), strings.Index(s, markEnd)+len(markEnd)
		if !ownShared {
			rep.add("preserve block: legacy repository ownership unverified: %s", p)
			continue
		}
		next := s[:i] + s[j:]
		changes = append(changes, change{path: p, before: b, after: []byte(next)})
	}
	for _, p := range []string{claudeSettings(), codexHooks(), agyHooks()} {
		if info, e := os.Lstat(p); e == nil && !info.Mode().IsRegular() {
			rep.add("preserve non-regular config: %s", p)
			continue
		}
		b, e := os.ReadFile(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return rep, e
		}
		var doc map[string]any
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		e = dec.Decode(&doc)
		if e != nil || doc == nil || dec.Decode(new(any)) != io.EOF {
			return rep, fmt.Errorf("malformed config: %s; unchanged", p)
		}
		// Reject malformed hook structure rather than silently dropping unknown data.
		clean := func(value any) (any, bool, error) {
			hooks, ok := value.(map[string]any)
			if !ok {
				return nil, false, fmt.Errorf("invalid hooks object: %s", p)
			}
			changed := false
			for _, event := range []string{"PreToolUse", "UserPromptSubmit"} {
				v, exists := hooks[event]
				if !exists {
					continue
				}
				groups, ok := v.([]any)
				if !ok {
					return nil, false, fmt.Errorf("invalid event groups: %s", p)
				}
				eventChanged := false
				kept := make([]any, 0, len(groups))
				for _, g := range groups {
					gm, ok := g.(map[string]any)
					if !ok {
						return nil, false, fmt.Errorf("invalid hook group: %s", p)
					}
					inner, ok := gm["hooks"].([]any)
					if !ok {
						return nil, false, fmt.Errorf("invalid inner hooks: %s", p)
					}
					hs := make([]any, 0, len(inner))
					for _, h := range inner {
						hm, ok := h.(map[string]any)
						if !ok {
							return nil, false, fmt.Errorf("invalid hook: %s", p)
						}
						if owned(hm["command"]) {
							changed = true
							eventChanged = true
						} else {
							hs = append(hs, h)
						}
					}
					if len(hs) > 0 || len(inner) == 0 {
						gm["hooks"] = hs
						kept = append(kept, gm)
					}
				}
				if len(kept) == 0 && eventChanged {
					delete(hooks, event)
				} else {
					hooks[event] = kept
				}
			}
			return hooks, changed, nil
		}
		changed := false
		for _, key := range []string{"hooks", "orch-guard"} {
			if v, ok := doc[key]; ok {
				v, ch, e := clean(v)
				if e != nil {
					return rep, e
				}
				if ch {
					changed = true
					if len(v.(map[string]any)) == 0 {
						delete(doc, key)
					} else {
						doc[key] = v
					}
				}
			}
		}
		if changed {
			var encoded bytes.Buffer
			enc := json.NewEncoder(&encoded)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(doc); err != nil {
				return rep, err
			}
			changes = append(changes, change{path: p, before: b, after: encoded.Bytes()})
		}
	}
	links := map[string]string{filepath.Join(orchHome(), "playbooks"): filepath.Join(repo, "playbooks"), filepath.Join(home(), ".local/bin/orch"): filepath.Join(repo, "bin/orch")}
	for _, p := range skillLinks() {
		links[p] = filepath.Join(repo, "skill")
	}
	for p, target := range links {
		cur, e := os.Readlink(p)
		if e != nil {
			if _, e = os.Lstat(p); e == nil {
				rep.add("preserve non-link: %s", p)
			}
			continue
		}
		if cur != target {
			rep.add("preserve foreign link: %s", p)
			continue
		}
		changes = append(changes, change{path: p, link: cur})
	}
	// Release only the matching manifest, last, after all owned changes succeed.
	if manifest != nil {
		info, e := os.Lstat(ownershipPath())
		if e != nil || !info.Mode().IsRegular() {
			return rep, fmt.Errorf("non-regular ownership manifest preserved: %s", ownershipPath())
		}
		b, e := os.ReadFile(ownershipPath())
		if e != nil {
			return rep, e
		}
		changes = append(changes, change{path: ownershipPath(), before: b, remove: true})
	}
	bdir := filepath.Join(orchHome(), "backups", "uninstall-"+time.Now().Format("20060102-150405.000000000"))
	// Recheck all snapshots before mutation; no historical whole-file restoration.
	if apply {
		for _, c := range changes {
			if c.link != "" {
				v, e := os.Readlink(c.path)
				if e != nil || v != c.link {
					return rep, fmt.Errorf("changed during planning: %s", c.path)
				}
			} else {
				v, e := os.ReadFile(c.path)
				if e != nil || !bytes.Equal(v, c.before) {
					return rep, fmt.Errorf("changed during planning: %s", c.path)
				}
			}
		}
		for _, c := range changes {
			if e := backup(bdir, c.path); e != nil {
				return rep, e
			}
		}
	}
	for _, c := range changes {
		rep.add("remove owned content: %s", c.path)
		if !apply {
			continue
		}
		if c.link != "" || c.remove {
			err = os.Remove(c.path)
		} else {
			err = atomicStateWrite(c.path, c.after)
		}
		if err != nil {
			return rep, err
		}
	}
	if apply && len(changes) > 0 {
		rep.add("backup: %s", bdir)
	}
	rep.add("runs, goals and measurements preserved; Codex /hooks trust must be removed manually")
	return rep, nil
}

// Refuse replacement of a real directory or another installation's symlink.
func safeInstallLink(path, target string) error {
	cur, err := os.Readlink(path)
	if err == nil {
		if cur == target {
			return nil
		}
		return fmt.Errorf("foreign symlink preserved: %s -> %s", path, cur)
	}
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("non-link preserved: %s", path)
}

func validHookConfig(s map[string]any) error {
	if s == nil {
		return fmt.Errorf("null hook configuration")
	}
	for _, key := range []string{"hooks", "orch-guard"} {
		if v, ok := s[key]; ok {
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid %s object", key)
			}
			for _, event := range []string{"PreToolUse", "UserPromptSubmit"} {
				if value, ok := m[event]; ok {
					groups, ok := value.([]any)
					if !ok {
						return fmt.Errorf("invalid %s groups", event)
					}
					for _, g := range groups {
						gm, ok := g.(map[string]any)
						if !ok {
							return fmt.Errorf("invalid hook group")
						}
						hs, ok := gm["hooks"].([]any)
						if !ok {
							return fmt.Errorf("invalid hooks list")
						}
						for _, h := range hs {
							if _, ok := h.(map[string]any); !ok {
								return fmt.Errorf("invalid inner hook")
							}
						}
					}
				}
			}
		}
	}
	return nil
}
