package chatcompletions

import (
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
)

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// thinkState tracks the inline <think> tag parser across chunks.
type thinkState struct {
	buf     string // un-emitted text (may contain a partial tag)
	inThink bool
}

// processThinkTags splits inline `<think>...</think>` tags from text content
// across chunk boundaries, emitting ThinkingDelta inside tags and TextDelta
// outside. Ported from Rust stream.rs think-tag state machine.
func processThinkTags(text string, ts *thinkState) []llm.StreamDelta {
	ts.buf += text
	var out []llm.StreamDelta
	for {
		if !ts.inThink {
			idx := strings.Index(ts.buf, thinkOpen)
			if idx >= 0 {
				if idx > 0 {
					out = append(out, &llm.TextDelta{Text: ts.buf[:idx]})
				}
				ts.buf = ts.buf[idx+len(thinkOpen):]
				ts.inThink = true
				continue
			}
			// No full tag; check if buf ends with a prefix of <think>.
			safe := len(ts.buf) - longestSuffixPrefix(ts.buf, thinkOpen)
			if safe > 0 {
				out = append(out, &llm.TextDelta{Text: ts.buf[:safe]})
				ts.buf = ts.buf[safe:]
			}
			return out
		}
		// Inside <think>; look for </think>.
		idx := strings.Index(ts.buf, thinkClose)
		if idx >= 0 {
			if idx > 0 {
				out = append(out, &llm.ThinkingDelta{Text: ts.buf[:idx]})
			}
			ts.buf = ts.buf[idx+len(thinkClose):]
			ts.inThink = false
			continue
		}
		safe := len(ts.buf) - longestSuffixPrefix(ts.buf, thinkClose)
		if safe > 0 {
			out = append(out, &llm.ThinkingDelta{Text: ts.buf[:safe]})
			ts.buf = ts.buf[safe:]
		}
		return out
	}
}

// flushThink emits any remaining buffered text (called on stream end).
func flushThink(ts *thinkState) []llm.StreamDelta {
	if ts.buf == "" {
		return nil
	}
	text := ts.buf
	ts.buf = ""
	if ts.inThink {
		return []llm.StreamDelta{&llm.ThinkingDelta{Text: text}}
	}
	return []llm.StreamDelta{&llm.TextDelta{Text: text}}
}

// longestSuffixPrefix returns the length of the longest suffix of s that is a
// prefix of tag (for detecting partial tags spanning chunk boundaries).
func longestSuffixPrefix(s, tag string) int {
	max := len(tag) - 1
	if max > len(s) {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasPrefix(tag, s[len(s)-n:]) {
			return n
		}
	}
	return 0
}
