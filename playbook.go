package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type Lane struct {
	Model    string   `yaml:"model"`
	Required bool     `yaml:"required"`
	When     string   `yaml:"when"`
	Withhold []string `yaml:"withhold"`
}

type Stage struct {
	Parallel     string          `yaml:"parallel"`
	Model        string          `yaml:"model"`
	Purpose      string          `yaml:"purpose"`
	When         string          `yaml:"when"`
	Output       string          `yaml:"output"`
	EscalateTo   string          `yaml:"escalate_to"`
	EscalateWhen []string        `yaml:"escalate_when"`
	Start        string          `yaml:"start"`
	Lanes        map[string]Lane `yaml:"lanes"`
	Owner        string          `yaml:"owner"`
	Mechanics    string          `yaml:"mechanics"`
	Notes        string          `yaml:"notes"`
}

type Playbook struct {
	Type        string           `yaml:"type"`
	DefaultMode string           `yaml:"default_mode"`
	Description string           `yaml:"description"`
	Stages      map[string]Stage `yaml:"stages"`
	RetryLimit  int              `yaml:"retry_limit"`
}

var (
	modes      = []string{"direct", "light", "full", "full+"}
	stageNames = []string{"recon", "design", "produce", "verify", "integrate"}
	laneNames  = []string{"mechanical", "critical", "neutral", "adversarial"}
	// A lane model is a catalog key, "head", or other_family(<refs>).
	otherFamily = regexp.MustCompile(`^other_family\((author|author,\s*critical)\)$`)
)

func validModelRef(s string) bool {
	if s == "" || s == "head" || otherFamily.MatchString(s) {
		return true
	}
	for _, alt := range strings.Split(s, "|") {
		if _, err := modelByKey(strings.TrimSpace(alt)); err != nil {
			return false
		}
	}
	return true
}

func validatePlaybook(p Playbook) []string {
	var errs []string
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(p.Type+": "+format, a...)) }
	if p.Type == "" {
		errs = append(errs, "type missing")
	}
	if !slices.Contains(modes, p.DefaultMode) {
		bad("default_mode %q must be one of %v", p.DefaultMode, modes)
	}
	if len(p.Stages) == 0 {
		bad("stages missing")
	}
	for name, st := range p.Stages {
		if !slices.Contains(stageNames, name) {
			bad("unknown stage %q", name)
		}
		for field, ref := range map[string]string{"model": st.Model, "escalate_to": st.EscalateTo, "mechanics": st.Mechanics} {
			if !validModelRef(ref) {
				bad("%s.%s %q is not a catalog key", name, field, ref)
			}
		}
		if st.Purpose != "" {
			if _, ok := chains[st.Purpose]; !ok {
				bad("%s.purpose %q is not in a fallback chain", name, st.Purpose)
			}
		}
		for lname, lane := range st.Lanes {
			if !slices.Contains(laneNames, lname) {
				bad("unknown lane %q", lname)
			}
			if !validModelRef(lane.Model) {
				bad("lane %s.model %q is not a catalog key", lname, lane.Model)
			}
			if lname == "adversarial" && lane.Model == "gemini-medium-m" {
				bad("gemini-medium-m cannot be used for an adversarial lane")
			}
			if lname == "critical" && lane.Model != "" && !strings.HasPrefix(lane.Model, "other_family") {
				bad("critical lane must use other_family(author)")
			}
		}
	}
	if p.RetryLimit < 0 || p.RetryLimit > 2 {
		bad("retry_limit must be 0–2 (design §10.5)")
	}
	return errs
}

func loadPlaybook(path string) (Playbook, error) {
	var p Playbook
	data, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return p, nil
}

func checkPlaybooks(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if len(files) == 0 {
		return []string{"playbook missing: " + dir}
	}
	var errs []string
	for _, f := range files {
		p, err := loadPlaybook(f)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if want := strings.TrimSuffix(filepath.Base(f), ".yaml"); p.Type != want {
			errs = append(errs, fmt.Sprintf("%s: type %q differs from the filename", filepath.Base(f), p.Type))
		}
		errs = append(errs, validatePlaybook(p)...)
	}
	return errs
}
