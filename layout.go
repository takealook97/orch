package main

import (
	"fmt"
	"math"
	"sort"
)

// Layout invariants (design §5.5, revised):
//
//	I1 the left column holds only the Head pane at full height
//	I2 the root split is left/right with the Head at 1/3 of the width
//	I3 workers live only in the right 2/3 column, stacked top to bottom
//	I4 right rows differ in height by at most one terminal row
//	I5 every spawn uses --no-focus
//	I6 panes the user opened are never moved or closed
const headRatio = 1.0 / 3.0

const (
	minRowHeight = 8
	maxRows      = 6
)

func rowCap(l Layout) int {
	n := l.Area.Height / minRowHeight
	if n > maxRows {
		n = maxRows
	}
	if n < 1 {
		n = 1
	}
	return n
}

type resizeOp struct {
	Pane      string
	Direction string
	Amount    float64
}

func findPane(l Layout, id string) (LayoutPane, bool) {
	for _, p := range l.Panes {
		if p.ID == id {
			return p, true
		}
	}
	return LayoutPane{}, false
}

func contains(outer, inner Rect) bool {
	return inner.X >= outer.X && inner.Y >= outer.Y &&
		inner.X+inner.Width <= outer.X+outer.Width && inner.Y+inner.Height <= outer.Y+outer.Height
}

// rightColumn returns the panes to the right of the Head, sorted top to bottom.
func rightColumn(l Layout, head LayoutPane) []LayoutPane {
	var out []LayoutPane
	for _, p := range l.Panes {
		if p.ID != head.ID && p.Rect.X >= head.Rect.X+head.Rect.Width {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rect.Y < out[j].Rect.Y })
	return out
}

// checkLayout reports invariant violations that resizing cannot repair.
func checkLayout(l Layout, headID string) error {
	head, ok := findPane(l, headID)
	if !ok {
		return fmt.Errorf("Head pane %s is absent from the current tab layout", headID)
	}
	if len(l.Panes) == 1 {
		return nil
	}
	if head.Rect.X != l.Area.X || head.Rect.Y != l.Area.Y || head.Rect.Height != l.Area.Height {
		return fmt.Errorf("I1 violation: Head does not occupy the full left column (rect=%+v)", head.Rect)
	}
	right := rightColumn(l, head)
	if len(right)+1 != len(l.Panes) {
		return fmt.Errorf("I1 violation: another pane is in the left column")
	}
	for _, p := range right {
		if p.Rect.X != right[0].Rect.X || p.Rect.Width != right[0].Rect.Width {
			return fmt.Errorf("I3 violation: the right column has a horizontal split")
		}
	}
	return nil
}

// planResize computes the ratio corrections for I2 and I4 from a single layout snapshot.
func planResize(l Layout, headID string) []resizeOp {
	head, ok := findPane(l, headID)
	if !ok || len(l.Panes) < 2 {
		return nil
	}
	var ops []resizeOp
	right := rightColumn(l, head)
	if len(right) == 0 {
		return nil
	}
	colX := right[0].Rect.X
	for _, s := range l.Splits {
		switch {
		case s.Direction == "right" && s.Rect == l.Area:
			delta := headRatio - s.Ratio
			if math.Abs(delta) > 0.002 {
				dir := "right"
				if delta < 0 {
					dir = "left"
				}
				ops = append(ops, resizeOp{head.ID, dir, math.Abs(delta)})
			}
		case s.Direction == "down" && s.Rect.X >= colX:
			var inside []LayoutPane
			for _, p := range right {
				if contains(s.Rect, p.Rect) {
					inside = append(inside, p)
				}
			}
			if len(inside) < 2 {
				continue
			}
			boundary := float64(s.Rect.Y) + s.Ratio*float64(s.Rect.Height)
			var first, second []LayoutPane
			for _, p := range inside {
				if float64(p.Rect.Y) < boundary-0.5 {
					first = append(first, p)
				} else {
					second = append(second, p)
				}
			}
			if len(first) == 0 || len(second) == 0 {
				continue
			}
			target := float64(len(first)) / float64(len(inside))
			delta := target - s.Ratio
			if math.Abs(delta) <= 0.002 {
				continue
			}
			if delta < 0 {
				ops = append(ops, resizeOp{second[0].ID, "up", -delta})
			} else {
				ops = append(ops, resizeOp{first[len(first)-1].ID, "down", delta})
			}
		}
	}
	return ops
}

// layoutOK checks I2 and I4 numerically.
func layoutOK(l Layout, headID string) bool {
	head, ok := findPane(l, headID)
	if !ok {
		return false
	}
	if len(l.Panes) == 1 {
		return true
	}
	want := float64(l.Area.Width) * headRatio
	if math.Abs(float64(head.Rect.Width)-want) > 1.5 {
		return false
	}
	right := rightColumn(l, head)
	lo, hi := math.MaxInt, 0
	for _, p := range right {
		lo = min(lo, p.Rect.Height)
		hi = max(hi, p.Rect.Height)
	}
	return hi-lo <= 1
}

// normalizeLayout repairs ratios and verifies the result (two repair rounds).
//
// Herdr moves UI focus to a pane's workspace and tab on every pane.zoom request, even when
// nothing is zoomed, which drags the user back to the Head from wherever they are. So zoom
// is turned off only when the layout reports it zoomed. pane.resize does not move focus.
func normalizeLayout(headID string) error {
	for round := 0; round < 3; round++ {
		l, err := getLayout(headID)
		if err != nil {
			return err
		}
		if l.Zoomed {
			_, _ = herdrRun("pane", "zoom", "--pane", headID, "--off")
			continue
		}
		if err := checkLayout(l, headID); err != nil {
			return err
		}
		if layoutOK(l, headID) {
			return nil
		}
		if round == 2 {
			break
		}
		for _, op := range planResize(l, headID) {
			if _, err := herdrRun("pane", "resize", "--pane", op.Pane, "--direction", op.Direction, "--amount", fmt.Sprintf("%.4f", op.Amount)); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("could not restore layout within two attempts (Head 1/3, equal rows on the right). Stop new spawns")
}
