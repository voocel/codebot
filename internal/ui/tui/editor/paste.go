package editor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Pastes longer than pasteInline runes enter the input as a reference like
// "[Pasted text #2 +40 lines]", expanded on send. This keeps the input
// readable; the transcript shows the full text.
const pasteInline = 1000

const pasteUnavailable = "[Pasted text unavailable]"

var (
	pasteRef        = regexp.MustCompile(`\[Pasted text #(\d+)(?: \+\d+ lines)?\]`)
	pasteRefAtEnd   = regexp.MustCompile(`\[Pasted text #\d+(?: \+\d+ lines)?\]$`)
	pasteRefAtStart = regexp.MustCompile(`^\[Pasted text #\d+(?: \+\d+ lines)?\]`)
)

type pastes struct {
	bodies map[int]string
	next   int
}

func (p *pastes) ref(body string) string {
	id := p.hold(body)
	if n := strings.Count(body, "\n"); n > 0 {
		return fmt.Sprintf("[Pasted text #%d +%d lines]", id, n+1)
	}
	return fmt.Sprintf("[Pasted text #%d]", id)
}

func (p *pastes) hold(body string) int {
	for id, b := range p.bodies {
		if b == body {
			return id
		}
	}
	if p.bodies == nil {
		p.bodies = map[int]string{}
	}
	p.next++
	p.bodies[p.next] = body
	return p.next
}

func (p *pastes) expand(text string) string {
	return pasteRef.ReplaceAllStringFunc(text, func(ref string) string {
		id, _ := strconv.Atoi(pasteRef.FindStringSubmatch(ref)[1])
		if b, ok := p.bodies[id]; ok {
			return b
		}
		return ref
	})
}

func (p *pastes) of(text string) map[int]string {
	var out map[int]string
	for _, m := range pasteRef.FindAllStringSubmatch(text, -1) {
		id, _ := strconv.Atoi(m[1])
		if b, ok := p.bodies[id]; ok {
			if out == nil {
				out = map[int]string{}
			}
			out[id] = b
		}
	}
	return out
}

// adopt re-registers a history entry's paste bodies under this session's
// ids and returns its text with the references renumbered.
func (p *pastes) adopt(e entry) string {
	return pasteRef.ReplaceAllStringFunc(e.text, func(ref string) string {
		id, _ := strconv.Atoi(pasteRef.FindStringSubmatch(ref)[1])
		switch b, ok := e.pasted[id]; {
		case !ok:
			return ref
		case b == "":
			return pasteUnavailable
		default:
			return p.ref(b)
		}
	})
}

// refBefore returns the rune length of a reference ending at col, or 0.
func refBefore(line []rune, col int) int {
	if col <= 0 || col > len(line) {
		return 0
	}
	before := string(line[:col])
	loc := pasteRefAtEnd.FindStringIndex(before)
	if loc == nil {
		return 0
	}
	return len([]rune(before[loc[0]:]))
}

// refAfter returns the rune length of a reference starting at col, or 0.
func refAfter(line []rune, col int) int {
	if col < 0 || col >= len(line) {
		return 0
	}
	return len([]rune(pasteRefAtStart.FindString(string(line[col:]))))
}
