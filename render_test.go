package main

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunks(t *testing.T) {
	tests := []struct {
		name  string
		avail int
		s     string
		want  []string
	}{
		{name: "no wrapping when avail is 0", avail: 0, s: strings.Repeat("x", 500), want: []string{strings.Repeat("x", 500)}},
		{name: "short line untouched", avail: 40, s: "OrderService/fetchAggregationSummary - start", want: []string{"OrderService/fetchAggregationSummary - s", "tart"}},
		{name: "exact fit is one chunk", avail: 5, s: "abcde", want: []string{"abcde"}},
		{name: "ascii split at avail", avail: 4, s: "abcdefghij", want: []string{"abcd", "efgh", "ij"}},
		{name: "empty string", avail: 10, s: "", want: []string{""}},
		{name: "multi-byte runes counted as one column", avail: 4, s: "€€€€€€", want: []string{"€€€€", "€€"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &renderer{avail: tt.avail}
			if got := r.chunks(tt.s); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("chunks(%q) = %q, want %q", tt.s, got, tt.want)
			}
		})
	}
}

// Wrap points must never split a multi-byte rune (was rendering mojibake),
// and no bytes may be lost - including on input that is already invalid
// UTF-8, which the service logger produces by truncating giant socket dumps
// mid-character.
func TestChunksRuneSafeAndLossless(t *testing.T) {
	inputs := []string{
		strings.Repeat("x", 1403), // real truncated-dump length
		strings.Repeat("€", 40),
		strings.Repeat("🙂", 40),
		strings.Repeat("א", 41),
		"[Symbo" + string([]byte{0xe2, 0x82}),                         // truncated mid-rune, like the real logs
		string([]byte{0xff, 0xfe, 0xfd}) + strings.Repeat("junk", 50), // raw invalid bytes
	}
	for _, s := range inputs {
		valid := utf8.ValidString(s)
		for _, avail := range []int{1, 7, 20, 63, 147} {
			r := &renderer{avail: avail}
			out := r.chunks(s)
			if got := strings.Join(out, ""); got != s {
				t.Fatalf("avail=%d: lost bytes (%d in, %d out)", avail, len(s), len(got))
			}
			if !valid {
				continue // invalid input stays invalid; only require losslessness
			}
			for i, c := range out {
				if !utf8.ValidString(c) {
					t.Fatalf("avail=%d: chunk %d split a rune: %q", avail, i, c)
				}
			}
		}
	}
}

func TestNormalizeBlock(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  []string
	}{
		{name: "nil in nil out", lines: nil, want: nil},
		{name: "common indent removed, nesting kept", lines: []string{"    at a()", "        at b()"}, want: []string{"at a()", "    at b()"}},
		{name: "tabs expanded before measuring", lines: []string{"\tat a()", "\t\tat b()"}, want: []string{"at a()", "    at b()"}},
		{name: "trailing CR stripped", lines: []string{"  done\r"}, want: []string{"done"}},
		{name: "blank lines ignored for indent, kept in output", lines: []string{"    a", "", "    b"}, want: []string{"a", "", "b"}},
		{name: "unindented block untouched", lines: []string{"a", "  b"}, want: []string{"a", "  b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeBlock(tt.lines); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("normalizeBlock(%q) = %q, want %q", tt.lines, got, tt.want)
			}
		})
	}
}
