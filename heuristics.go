package main

import (
	"regexp"
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

func quotaText(s string) bool {
	s = strings.ToLower(s)
	for _, m := range quotaMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// promptMarker prefixes every worker prompt so delivery can be confirmed in the transcript.
func promptMarker(name string) string { return "[orch worker " + name + "]" }
