package api

// Log stream hygiene. Serial consoles and guest journals are control-sequence
// firehoses: ANSI escapes render as garbage ("malformed"), CR-flavored lines
// reflow oddly, and one giant line (a 200 kB embed or a wall of kernel text)
// is enough to crush a browser tab's DOM/reflow. Every VM-side stream source
// (docker serial tail, libvirt capture file, guest journal over ssh) sends
// its bytes through here before they become SSE frames; container streams
// benefit too (crash dumps come in colored).

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ansiStrip covers CSI sequences, OSC through bell/ST, and loose C1 control
// bytes (\x1b[@-Z\_]) — the shapes qemu serial / journald / crash dumps emit.
var ansiStrip = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// lineCap: anything wider than this in a serial/journal line is noise, not
// output the user can act on. Capped instead of streamed to keep the DOM sane.
const lineCap = 4000

// sanitizeLine makes one raw stream line browser-safe.
func sanitizeLine(s string) string {
	s = ansiStrip.ReplaceAllString(s, "")
	s = strings.Trim(s, "\r")
	s = strings.ReplaceAll(s, "\r", " ")
	// terminal UTF-8 noise must never split mid-rune
	s = strings.ToValidUTF8(s, "?")
	// anything the escape-strip missed (bare ETX/ESC halves of split binary
	// blobs, C1 from 8-bit noise, DEL) still renders as garbage glyphs —
	// printable + tab only
	s = strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
	if n := utf8.RuneCountInString(s); n > lineCap {
		cut := len(s)
		seen := 0
		for bi := range s {
			if seen == lineCap {
				cut = bi
				break
			}
			seen++
		}
		s = s[:cut] + fmt.Sprintf("  …(+%d kB line truncated)", (n-lineCap)/1000)
	}
	return s
}

// lineFlusher re-chunks an arbitrary byte stream into single-line payloads and
// sends each through a callback; partial lines survive between Write calls.
// Blank spam is dropped, and a newline-free flood (>512KiB) is flushed capped
// so silent binary streams cannot buffer without bound.
type lineFlusher struct {
	send  func(string)
	carry []byte
}

func newLineFlusher(send func(string)) *lineFlusher {
	return &lineFlusher{send: send, carry: make([]byte, 0, 8192)}
}

func (l *lineFlusher) Write(p []byte) (int, error) {
	l.carry = append(l.carry, p...)
	for idx := bytes.IndexByte(l.carry, '\n'); idx >= 0; idx = bytes.IndexByte(l.carry, '\n') {
		l.emit(l.carry[:idx])
		rest := append([]byte(nil), l.carry[idx+1:]...)
		l.carry = l.carry[:0]
		l.carry = append(l.carry, rest...)
	}
	if len(l.carry) > 512<<10 {
		l.emit(l.carry)
		l.carry = l.carry[:0]
	}
	return len(p), nil
}

func (l *lineFlusher) emit(raw []byte) {
	line := sanitizeLine(string(raw))
	if strings.TrimSpace(line) == "" {
		return // blank spam collapses; the UI already caps node count
	}
	l.send(line)
}
