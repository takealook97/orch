package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Everything orch infers from another CLI's screen or timing lives here. These are
// heuristics: when Claude, Codex or agy change their UI text or startup speed, this is the
// one file to update.

// timings holds every wait and poll interval used while driving worker agents.
var timings = struct {
	TrustSettleStart  time.Duration // watch for a folder-trust dialog right after agent start
	TrustSettleReady  time.Duration // watch again once the agent reports ready
	TrustSettlePrompt time.Duration // watch again just before typing the prompt
	TrustAccepted     time.Duration // let the CLI redraw after a trust dialog was answered
	StartRetry        time.Duration // pause before the single agent-start retry
	ReadyTimeout      time.Duration // how long a started agent may take to become ready
	KeyGap            time.Duration // pause between typed text and the Enter key
	Delivery          time.Duration // how long to look for proof that a prompt arrived
	Poll              time.Duration // short poll interval (trust, delivery)
	ReadyPoll         time.Duration // poll interval while waiting for readiness
	WaitPoll          time.Duration // poll interval of orch wait
	ScreenRead        time.Duration // how often orch wait reads screens (quota, stray panes)
	IdleNoResult      time.Duration // idle this long without a result file means stalled
	DagPoll           time.Duration // poll interval of orch dag run
	DagStartWait      time.Duration // wait budget right after orch dag run starts a node
	DagWait           time.Duration // wait budget per loop of orch dag run
	ReapPoll          time.Duration // poll interval of the per-run reaper
	ReapIdle          time.Duration // the reaper exits after this long without live workers
	TaskHeartbeat     time.Duration // how often a running task refreshes its heartbeat
	TaskStale         time.Duration // a running task whose heartbeat is older than this has died
	InterruptGrace    time.Duration // how long an interrupted task child may take to exit
}{
	TrustSettleStart:  8 * time.Second,
	TrustSettleReady:  3 * time.Second,
	TrustSettlePrompt: 3 * time.Second,
	TrustAccepted:     2 * time.Second,
	StartRetry:        2 * time.Second,
	ReadyTimeout:      60 * time.Second,
	KeyGap:            300 * time.Millisecond,
	Delivery:          20 * time.Second,
	Poll:              time.Second,
	ReadyPoll:         2 * time.Second,
	WaitPoll:          3 * time.Second,
	ScreenRead:        15 * time.Second,
	IdleNoResult:      45 * time.Second,
	DagPoll:           5 * time.Second,
	DagStartWait:      time.Second,
	DagWait:           20 * time.Second,
	ReapPoll:          3 * time.Second,
	ReapIdle:          60 * time.Second,
	TaskHeartbeat:     10 * time.Second,
	TaskStale:         60 * time.Second,
	InterruptGrace:    2 * time.Second,
}

// Consecutive polls reporting "working" that count as proof a prompt was received.
const workingPollsForDelivery = 3

// trustDialog matches the folder-trust dialogs of Claude, Codex and agy.
var trustDialog = regexp.MustCompile(`(?i)(do you trust|trust this folder|trust the files|trust this directory)`)

// claudeTrustChoice marks Claude's trust dialog, whose default choice is "No, exit".
const claudeTrustChoice = "Yes, I trust"

// quotaMarkers are lowercase fragments of usage-limit messages across the three CLIs.
var quotaMarkers = []string{
	"you've hit your usage limit", "you have hit your usage limit", "usage limit reached",
	"you've hit your limit", "you are out of usage", "quota exceeded", "rate limit exceeded",
	"resource_exhausted", "resource has been exhausted", "credit balance is too low",
}

// crExhaustedMarkers are the cr wrapper's own lines: every account of that CLI is spent,
// so a fallback has to leave the CLI kind instead of rotating accounts.
var crExhaustedMarkers = []string{"cr: all codex accounts reached capacity", "cr: capacity exhausted"}

// screenTailLines is how many non-empty lines at the bottom of a screen count as current.
const screenTailLines = 12

// tailLines returns the last screenTailLines non-empty lines that are not tool output.
// Tool output is a line starting with │ or └, plus the indented lines that follow one or
// follow a "• Ran" header, until a line starts at column zero again.
func tailLines(s string) []string {
	var kept []string
	inTool := false
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		switch {
		case strings.HasPrefix(trimmed, "│") || strings.HasPrefix(trimmed, "└"):
			inTool = true
			continue
		case strings.HasPrefix(trimmed, "• Ran"):
			inTool = true
		case !indented:
			inTool = false
		case inTool:
			continue
		}
		kept = append(kept, trimmed)
	}
	if len(kept) > screenTailLines {
		kept = kept[len(kept)-screenTailLines:]
	}
	return kept
}

func containsMarker(lines []string, markers []string) bool {
	for _, line := range lines {
		low := strings.ToLower(line)
		for _, m := range markers {
			if strings.Contains(low, m) {
				return true
			}
		}
	}
	return false
}

func quotaText(s string) bool {
	lines := tailLines(s)
	return containsMarker(lines, quotaMarkers) || containsMarker(lines, crExhaustedMarkers)
}

// crExhausted reports the cr wrapper's all-accounts-spent lines in the current tail.
func crExhausted(s string) bool { return containsMarker(tailLines(s), crExhaustedMarkers) }

// promptMarker prefixes every worker prompt so delivery can be confirmed in the transcript.
func promptMarker(name string) string { return "[orch worker " + name + "]" }

// capacityText deliberately ignores old errors above the visible tail.
var capacityMarkers = []string{
	"selected model is at capacity", "model is at capacity", "please try a different model",
	"overloaded_error", "server is overloaded", "model is currently overloaded",
}

func capacityText(s string) bool { return containsMarker(tailLines(s), capacityMarkers) }

// capacityDelay is swappable by tests without shortening production backoffs.
var capacityDelay = func(n int) time.Duration {
	if n < 0 {
		n = 0
	}
	if n >= 4 {
		return 300 * time.Second
	}
	return (20 * time.Second) << n
}

func capacityBackoff(n int) time.Duration { return capacityDelay(n) }
func capacityMaxRetries() int {
	if n, err := strconv.Atoi(envOrch("CAPACITY_RETRIES")); err == nil && n >= 0 {
		return n
	}
	return 8
}

const capacityContinue = "The last turn stopped because the model was at capacity. Continue the contract from where you stopped; do not redo finished steps."

func capacityResumeText(name string) string { return promptMarker(name) + " " + capacityContinue }
