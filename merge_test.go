package main

import (
	"context"
	"sort"
	"testing"
	"time"
)

const bigHoldback = 200 * 365 * 24 * time.Hour

func lessKey(a, b *Entry) bool {
	if a.EpochMS != b.EpochMS {
		return a.EpochMS < b.EpochMS
	}
	if a.Service != b.Service {
		return a.Service < b.Service
	}
	return a.Seq < b.Seq
}

func TestMergeLiveOrdersAndDrains(t *testing.T) {
	base := int64(1_700_000_000_000)
	arrivals := []struct {
		service string
		epoch   int64
	}{
		{"charlie", base + 200},
		{"alpha", base + 50},
		{"bravo", base + 50},
		{"alpha", base + 50},
		{"charlie", base + 0},
		{"bravo", base + 120},
		{"alpha", base + 250},
		{"charlie", base + 120},
		{"bravo", base + 120},
		{"alpha", base + 0},
	}
	entries := make([]*Entry, 0, len(arrivals))
	for _, a := range arrivals {
		entries = append(entries, &Entry{Service: a.service, EpochMS: a.epoch, Seq: nextSeq()})
	}

	in := make(chan *Entry)
	var got []*Entry
	done := make(chan struct{})
	go func() {
		mergeLive(context.Background(), in, bigHoldback, func(e *Entry) { got = append(got, e) })
		close(done)
	}()
	for _, e := range entries {
		in <- e
	}
	close(in)
	<-done

	if len(got) != len(entries) {
		t.Fatalf("drain incomplete: got %d entries, want %d", len(got), len(entries))
	}

	want := make([]*Entry, len(entries))
	copy(want, entries)
	sort.Slice(want, func(i, j int) bool { return lessKey(want[i], want[j]) })

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch at %d: got {svc=%s epoch=%d seq=%d}, want {svc=%s epoch=%d seq=%d}", i, got[i].Service, got[i].EpochMS, got[i].Seq, want[i].Service, want[i].EpochMS, want[i].Seq)
		}
	}
	for i := 1; i < len(got); i++ {
		if lessKey(got[i], got[i-1]) {
			t.Fatalf("output not strictly ordered at %d", i)
		}
	}
}

func TestSortEntriesShuffled(t *testing.T) {
	base := int64(1_700_000_000_000)
	mk := func(service string, epoch int64) *Entry {
		return &Entry{Service: service, EpochMS: epoch, Seq: nextSeq()}
	}
	a1 := mk("alpha", base+10)
	a2 := mk("alpha", base+10)
	b1 := mk("bravo", base+10)
	c1 := mk("charlie", base+0)
	a0 := mk("alpha", base+0)
	b2 := mk("bravo", base+30)
	b3 := mk("bravo", base+30)
	c2 := mk("charlie", base+30)

	shuffled := []*Entry{b3, a0, c2, a1, b1, c1, b2, a2}
	sortEntries(shuffled)

	want := []*Entry{a0, c1, a1, a2, b1, b2, b3, c2}
	for i := range want {
		if shuffled[i] != want[i] {
			t.Fatalf("sortEntries wrong at %d: got {svc=%s epoch=%d seq=%d}", i, shuffled[i].Service, shuffled[i].EpochMS, shuffled[i].Seq)
		}
	}
	for i := 1; i < len(shuffled); i++ {
		if lessKey(shuffled[i], shuffled[i-1]) {
			t.Fatalf("sortEntries not strictly ordered at %d", i)
		}
	}
}
