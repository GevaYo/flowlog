package main

import (
	"reflect"
	"testing"
)

const testAlias = "Collect"

// Fixtures are synthetic lines in the winston format the parser expects:
// "<operation id> | <level> | <epoch ms> | <date> | <message> | <params>",
// with continuation lines indented under a header. emptyOpLine covers the
// no-operation-id case, where the line starts with " | ".
var (
	singleLine = "0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d | info | 1783247849821 | Sun Jul 05 2026 13:37:29 GMT+0300 (Israel Daylight Time) | OrderService/fetchAggregationSummary - start | [ [] ]"

	multiHeader = "0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d | verbose | 1783247849820 | Sun Jul 05 2026 13:37:29 GMT+0300 (Israel Daylight Time) | DbProvider/getUserByPk - end. user:  | ["
	multiCont1  = "  ["
	multiCont2  = "    RowDataPacket { user_phone: '+15551234567', user_id: 'f0e1d2c3-b4a5-6789-0123-456789abcdef', user_email: 'jane@example.com', first_name: 'John', last_name: 'Doe' }"
	multiCont3  = "  ]"
	multiCont4  = "]"

	emptyOpLine = " | warn | 1783247850000 | Sun Jul 05 2026 13:37:30 GMT+0300 (Israel Daylight Time) | ConfigService/loadDefaults - no org context | [ [] ]"

	// The double space in "user:  | [" leaves one trailing space on the
	// message after splitting off the " | " separator.
	multiMessage = "DbProvider/getUserByPk - end. user: "
)

func collect(lines []string) []*Entry {
	p := newParser(testAlias)
	var out []*Entry
	for _, ln := range lines {
		if e := p.feed(ln); e != nil {
			out = append(out, e)
		}
	}
	if e := p.flush(); e != nil {
		out = append(out, e)
	}
	return out
}

func TestParser(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  []*Entry
	}{
		{
			name:  "single line entry with operation id",
			lines: []string{singleLine},
			want:  []*Entry{{Service: testAlias, OperationID: "0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d", Level: "info", EpochMS: 1783247849821, Message: "OrderService/fetchAggregationSummary - start", Lines: []string{singleLine}}},
		},
		{
			name:  "multi line entry groups continuation lines",
			lines: []string{multiHeader, multiCont1, multiCont2, multiCont3, multiCont4},
			want:  []*Entry{{Service: testAlias, OperationID: "0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d", Level: "verbose", EpochMS: 1783247849820, Message: multiMessage, Lines: []string{multiHeader, multiCont1, multiCont2, multiCont3, multiCont4}}},
		},
		{
			name:  "empty operation id",
			lines: []string{emptyOpLine},
			want:  []*Entry{{Service: testAlias, OperationID: "", Level: "warn", EpochMS: 1783247850000, Message: "ConfigService/loadDefaults - no org context", Lines: []string{emptyOpLine}}},
		},
		{
			name:  "new header completes previous entry and flush returns the last",
			lines: []string{singleLine, multiHeader, multiCont1},
			want: []*Entry{
				{Service: testAlias, OperationID: "0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d", Level: "info", EpochMS: 1783247849821, Message: "OrderService/fetchAggregationSummary - start", Lines: []string{singleLine}},
				{Service: testAlias, OperationID: "0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d", Level: "verbose", EpochMS: 1783247849820, Message: multiMessage, Lines: []string{multiHeader, multiCont1}},
			},
		},
		{
			// A non-header line seen before any header has no entry to attach to,
			// so it is dropped: attached to nothing, no panic. This is the
			// simplest safe behavior for garbage preceding the first header.
			name:  "garbage line before first header is dropped",
			lines: []string{"stray continuation with no header", singleLine},
			want:  []*Entry{{Service: testAlias, OperationID: "0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d", Level: "info", EpochMS: 1783247849821, Message: "OrderService/fetchAggregationSummary - start", Lines: []string{singleLine}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collect(tt.lines)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("collect() mismatch\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

func TestFlushEmptyParser(t *testing.T) {
	if e := newParser(testAlias).flush(); e != nil {
		t.Fatalf("flush on fresh parser = %#v, want nil", e)
	}
}

// A continuation line arriving after an idle flush (the entry was emitted
// mid-write) resumes as a follow-up entry with the flushed entry's metadata,
// instead of being dropped.
func TestResumeAfterIdleFlush(t *testing.T) {
	p := newParser(testAlias)
	p.feed(multiHeader)
	if e := p.flush(); e == nil { // 500ms idle flush in tail.go
		t.Fatal("idle flush should emit the in-progress entry")
	}
	if e := p.feed(multiCont1); e != nil {
		t.Fatalf("continuation should not emit immediately, got %#v", e)
	}
	got := p.flush()
	want := &Entry{Service: testAlias, OperationID: "0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d", Level: "verbose", EpochMS: 1783247849820, Message: multiCont1, Lines: []string{multiCont1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resumed entry mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// reset (called on log rotation) drops the resume metadata, so a partial
// first line in the fresh file is dropped like any pre-header garbage
// instead of inheriting the pre-rotation entry's timestamp and level.
func TestResetDropsResumeMetadata(t *testing.T) {
	p := newParser(testAlias)
	p.feed(multiHeader)
	p.flush()
	p.reset()
	if e := p.feed("orphan tail line from the rotated file"); e != nil {
		t.Fatalf("feed after reset emitted %#v, want nil", e)
	}
	if e := p.flush(); e != nil {
		t.Fatalf("orphan line after reset produced entry %#v, want nil", e)
	}
}
