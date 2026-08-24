package main

import (
	"container/heap"
	"context"
	"sort"
	"time"
)

func entryLess(a, b *Entry) bool {
	if a.EpochMS != b.EpochMS {
		return a.EpochMS < b.EpochMS
	}
	if a.Service != b.Service {
		return a.Service < b.Service
	}
	return a.Seq < b.Seq
}

type entryHeap []*Entry

func (h entryHeap) Len() int           { return len(h) }
func (h entryHeap) Less(i, j int) bool { return entryLess(h[i], h[j]) }
func (h entryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *entryHeap) Push(x any)        { *h = append(*h, x.(*Entry)) }

func (h *entryHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return e
}

func mergeLive(ctx context.Context, in <-chan *Entry, holdback time.Duration, emit func(*Entry)) {
	h := &entryHeap{}
	heap.Init(h)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			for e := range in {
				heap.Push(h, e)
			}
			drainHeap(h, emit)
			return
		case e, ok := <-in:
			if !ok {
				drainHeap(h, emit)
				return
			}
			heap.Push(h, e)
		case <-ticker.C:
			cutoff := time.Now().UnixMilli() - holdback.Milliseconds()
			for h.Len() > 0 && (*h)[0].EpochMS < cutoff {
				emit(heap.Pop(h).(*Entry))
			}
		}
	}
}

func drainHeap(h *entryHeap, emit func(*Entry)) {
	for h.Len() > 0 {
		emit(heap.Pop(h).(*Entry))
	}
}

func sortEntries(es []*Entry) {
	sort.Slice(es, func(i, j int) bool { return entryLess(es[i], es[j]) })
}
