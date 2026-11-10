package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Provider structs intentionally contain no content, prompts, tool arguments or
// credentials. Unknown JSON fields are skipped and errors never echo records.
type providerTokens struct {
	Input      *int64 `json:"input_tokens"`
	Output     *int64 `json:"output_tokens"`
	CacheRead  *int64 `json:"cache_read_input_tokens"`
	CacheWrite *int64 `json:"cache_creation_input_tokens"`
}
type providerRecord struct {
	Type    string `json:"type"`
	Version *int   `json:"schema_version"`
	Session string `json:"sessionId"`
	Request string `json:"requestId"`
	Message struct {
		ID    string          `json:"id"`
		Model string          `json:"model"`
		Stop  string          `json:"stop_reason"`
		Usage *providerTokens `json:"usage"`
	} `json:"message"`
	Payload struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Model string `json:"model"`
		Info  *struct {
			Total *providerTokens `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}
type usageBinding struct{ Provider, File, Session, Subject, Entity, Period, Phase string }
type usageProvenance struct {
	Provider string `json:"provider"`
	Message  string `json:"message_id,omitempty"`
	Request  string `json:"request_id,omitempty"`
	Session  string `json:"provider_session"`
	Binding  string `json:"binding"`
	File     string `json:"file_fingerprint"`
}
type usageCollection struct {
	Provider            string        `json:"provider"`
	Binding             string        `json:"binding"`
	Lines               int           `json:"closed_lines"`
	Events              int           `json:"events"`
	Coverage            string        `json:"coverage"`
	Unclosed            bool          `json:"unclosed_record"`
	Duplicates          int           `json:"duplicate_observations"`
	DuplicateProvenance string        `json:"duplicate_provenance,omitempty"`
	Records             []Measurement `json:"records,omitempty"`
}

func digestID(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (b usageBinding) template() Measurement {
	return Measurement{Version: 1, ID: "validate", Session: b.Session, Subject: b.Subject, Entity: b.Entity, Period: b.Period, Phase: b.Phase, Source: b.Provider + "-session-v1", Kind: "measured", Coverage: "partial", Provenance: &usageProvenance{Provider: b.Provider, Session: b.Session, Binding: "operator-declared", File: digestID(b.File)}}
}
func collectUsage(b usageBinding, r io.Reader) (usageCollection, error) {
	result := usageCollection{Provider: b.Provider, Binding: "operator-declared", Coverage: "unavailable"}
	base := b.template()
	if !contains1([]string{"claude", "codex"}, b.Provider) {
		return result, fmt.Errorf("provider must be claude or codex")
	}
	if err := base.validate(); err != nil {
		return result, err
	}
	reader := bufio.NewReader(r)
	var previous *providerTokens
	model := ""
	seenSession := false
	unique := 0
	claude := map[string]Measurement{}
	requests := map[string]string{}
	messages := map[string]string{}
	var order []string
	// Final Claude messages are immutable observations, within and across batches.
	for {
		line, err := readUsageLine(reader)
		if err != nil && err != io.EOF {
			return result, err
		}
		if err == io.EOF {
			if len(line) > 0 {
				result.Unclosed = true
			}
			break
		}
		result.Lines++
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec providerRecord
		if json.Unmarshal(line, &rec) != nil {
			return result, fmt.Errorf("invalid provider JSON at line %d", result.Lines)
		}
		if rec.Version != nil && *rec.Version != 1 {
			return result, fmt.Errorf("unsupported provider version at line %d", result.Lines)
		}
		if b.Provider == "claude" {
			if rec.Session != "" {
				if rec.Session != b.Session {
					return result, fmt.Errorf("provider session binding mismatch")
				}
				seenSession = true
			}
			if rec.Type != "assistant" || rec.Message.Usage == nil {
				continue
			}
			// Claude local placeholders are not provider requests or usage evidence.
			// Check session binding above even for these non-provider rows.
			if rec.Message.Model == "<synthetic>" {
				continue
			}
			if rec.Message.ID == "" || !evidenceID.MatchString(rec.Message.ID) {
				return result, fmt.Errorf("missing or invalid message identity")
			}
			if rec.Session == "" {
				return result, fmt.Errorf("missing provider session identity on usage row")
			}
			if rec.Message.Stop == "" {
				continue
			} // Growing, unfinalized usage is not evidence.
			if rec.Request != "" {
				if !evidenceID.MatchString(rec.Request) {
					return result, fmt.Errorf("invalid request identity")
				}
				if old, ok := requests[rec.Request]; ok && old != rec.Message.ID {
					return result, fmt.Errorf("request/message identity conflict")
				}
				if old, ok := messages[rec.Message.ID]; ok && old != rec.Request {
					return result, fmt.Errorf("message/request identity conflict")
				}
				requests[rec.Request] = rec.Message.ID
				messages[rec.Message.ID] = rec.Request
			}
			m := base
			m.ID = "usage-" + digestID(b.Provider+":"+rec.Message.ID)
			provenance := *base.Provenance
			provenance.Message = rec.Message.ID
			provenance.Request = rec.Request
			m.Provenance = &provenance
			m.Model = rec.Message.Model
			in, out, e := normalizeProviderTokens(rec.Message.Usage, true)
			if e != nil {
				return result, e
			}
			m.Input = in
			m.Output = out
			if in == nil && out == nil {
				m.Kind = "missing"
				m.Coverage = "unavailable"
			}
			if e = m.validate(); e != nil {
				return result, fmt.Errorf("invalid provider metadata: %w", e)
			}
			// A message identity must not be silently reassigned to another request/model.
			if old, ok := claude[m.ID]; ok {
				if old.Model != m.Model || !sameCounter(old.Input, m.Input) || !sameCounter(old.Output, m.Output) || old.Provenance.Request != m.Provenance.Request {
					return result, fmt.Errorf("conflicting finalized message observation")
				}
			} else {
				order = append(order, m.ID)
			}
			claude[m.ID] = m
		} else {
			switch rec.Type {
			case "session_meta":
				if rec.Payload.ID != b.Session {
					return result, fmt.Errorf("provider session binding mismatch")
				}
				seenSession = true
			case "turn_context":
				model = rec.Payload.Model
			case "event_msg":
				if rec.Payload.Type != "token_count" || rec.Payload.Info == nil || rec.Payload.Info.Total == nil {
					continue
				}
				if !seenSession {
					return result, fmt.Errorf("usage before provider session binding")
				}
				total := rec.Payload.Info.Total
				in, out, e := normalizeProviderTokens(total, false)
				if e != nil {
					return result, e
				}
				if previous != nil && sameCounter(previous.Input, in) && sameCounter(previous.Output, out) {
					continue
				}
				// Missing dimensions remain unknown; never bridge a gap with guessed usage.
				delta := func(now, prev *int64) (*int64, error) {
					if now == nil || prev == nil {
						return nil, nil
					}
					if *now < *prev {
						return nil, fmt.Errorf("cumulative usage reset; split/reconcile session explicitly")
					}
					v := *now - *prev
					return &v, nil
				}
				di, do := in, out
				if previous != nil {
					di, e = delta(in, previous.Input)
					if e != nil {
						return result, e
					}
					do, e = delta(out, previous.Output)
					if e != nil {
						return result, e
					}
				}
				previous = &providerTokens{Input: in, Output: out}
				unique++
				m := base
				m.ID = "usage-" + digestID(b.Provider+":"+b.Session+fmt.Sprintf(":%d", unique))
				m.Input = di
				m.Output = do
				m.Model = model
				if di == nil && do == nil {
					m.Kind = "missing"
					m.Coverage = "unavailable"
				}
				if e = m.validate(); e != nil {
					return result, fmt.Errorf("invalid provider metadata: %w", e)
				}
				result.Records = append(result.Records, m)
			}
		}
	}
	for _, id := range order {
		result.Records = append(result.Records, claude[id])
	}
	result.Events = len(result.Records)
	for _, m := range result.Records {
		if m.Input != nil || m.Output != nil {
			result.Coverage = "partial"
			break
		}
	}
	return result, nil
}
func sameCounter(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func normalizeProviderTokens(t *providerTokens, claude bool) (*int64, *int64, error) {
	for _, v := range []*int64{t.Input, t.Output, t.CacheRead, t.CacheWrite} {
		if v != nil && *v < 0 {
			return nil, nil, fmt.Errorf("negative provider usage")
		}
	}
	in := t.Input
	if claude && in != nil {
		n := *in
		for _, v := range []*int64{t.CacheRead, t.CacheWrite} {
			if v != nil {
				if *v > math.MaxInt64-n {
					return nil, nil, fmt.Errorf("provider usage overflow")
				}
				n += *v
			}
		}
		// Claude cache fields are required to establish inclusive input semantics.
		if t.CacheRead == nil || t.CacheWrite == nil {
			in = nil
		} else {
			in = &n
		}
	}
	return in, t.Output, nil
}
func readUsageLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > 16<<20 {
			return nil, fmt.Errorf("provider record exceeds size limit")
		}
		line = append(line, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, err
	}
}
func collectUsageCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("measure collect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var b usageBinding
	fs.StringVar(&b.Provider, "provider", "", "")
	fs.StringVar(&b.File, "session-file", "", "")
	fs.StringVar(&b.Session, "session", "", "")
	fs.StringVar(&b.Subject, "subject", "", "")
	fs.StringVar(&b.Entity, "entity", "", "")
	fs.StringVar(&b.Period, "period", "", "")
	fs.StringVar(&b.Phase, "phase", "", "")
	export := fs.Bool("export", false, "")
	if fs.Parse(args) != nil || fs.NArg() != 0 || b.File == "" {
		return fmt.Errorf("measure collect requires --provider --session-file --session --subject --entity --period --phase; --export writes normalized evidence only")
	}
	if err := b.template().validate(); err != nil {
		return err
	}
	f, err := os.Open(b.File)
	if err != nil {
		return fmt.Errorf("cannot open provider session file")
	}
	defer f.Close()
	result, err := collectUsage(b, f)
	if err != nil {
		return err
	}
	if *export {
		return json.NewEncoder(out).Encode(result)
	}
	result.Duplicates, err = recordMeasurementBatchWithDuplicates(result.Records)
	if err != nil {
		return err
	}
	if result.Duplicates > 0 {
		result.DuplicateProvenance = "existing-observation-retained"
	}
	result.Records = nil
	return json.NewEncoder(out).Encode(result)
}

// Validate the complete batch and every conflict before writing anything. Same
// session/provider evidence cannot be rebound to another entity/phase/period.
func recordMeasurementBatch(batch []Measurement) error {
	_, err := recordMeasurementBatchWithDuplicates(batch)
	return err
}
func recordMeasurementBatchWithDuplicates(batch []Measurement) (int, error) {
	duplicates := 0
	for _, m := range batch {
		if err := m.validate(); err != nil {
			return 0, err
		}
	}
	if len(batch) == 0 {
		return 0, nil
	}
	p := measurementPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	rows, err := readMeasurements(f)
	if err != nil {
		return 0, err
	}
	var pending []Measurement
	for _, m := range batch {
		found := false
		for _, old := range append(rows, pending...) {
			globalClaude := old.Provenance != nil && m.Provenance != nil && old.Provenance.Provider == "claude" && m.Provenance.Provider == "claude"
			if globalClaude && old.Provenance.Message == "" && m.Provenance.Message != "" && old.ID == "usage-"+digestID("claude:"+old.Session+":"+m.Provenance.Message) {
				return 0, fmt.Errorf("legacy session-scoped Claude observation; reconcile existing provenance before collection")
			}
			sameMessage := globalClaude && old.Provenance.Message != "" && old.Provenance.Message == m.Provenance.Message
			sameRequest := globalClaude && old.Provenance.Request != "" && old.Provenance.Request == m.Provenance.Request
			if old.Session == m.Session && old.ID == m.ID || globalClaude && (old.ID == m.ID || sameMessage || sameRequest) {
				a, b := old, m
				a.At = time.Time{}
				b.At = time.Time{}
				// Moving the same file does not change the provider observation.
				if a.Provenance != nil && b.Provenance != nil {
					aa, bb := *a.Provenance, *b.Provenance
					aa.File = ""
					bb.File = ""
					if globalClaude {
						// Retain the first observed session provenance; identical
						// copies cannot acquire another usage attribution.
						a.Session = ""
						b.Session = ""
						aa.Session = ""
						bb.Session = ""
					}
					a.Provenance = &aa
					b.Provenance = &bb
				}
				x, _ := json.Marshal(a)
				y, _ := json.Marshal(b)
				if !bytes.Equal(x, y) {
					return 0, fmt.Errorf("conflicting collected evidence; existing binding or finalized usage differs")
				}
				found = true
				duplicates++
				break
			}
		}
		if !found {
			m.At = time.Now().UTC()
			pending = append(pending, m)
		}
	}
	var encoded bytes.Buffer
	for _, m := range pending {
		if err = json.NewEncoder(&encoded).Encode(m); err != nil {
			return 0, err
		}
	}
	if _, err = f.Write(encoded.Bytes()); err != nil {
		return 0, err
	}
	return duplicates, f.Sync()
}
