package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"time"
)

func tailService(ctx context.Context, svc Service, follow bool, sinceMS int64, out chan<- *Entry) {
	alias := svc.Alias
	if alias == "" {
		alias = svc.Name
	}
	logPath := svc.Path + "/rapyd.log"
	p := newParser(alias)

	send := func(e *Entry) bool {
		e.Seq = nextSeq()
		select {
		case out <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if !follow {
		f, err := os.Open(logPath)
		if err != nil {
			return
		}
		defer f.Close()
		reader := bufio.NewReader(f)
		for {
			line, rerr := reader.ReadString('\n')
			if len(line) > 0 {
				if e := p.feed(strings.TrimSuffix(line, "\n")); e != nil && e.EpochMS >= sinceMS {
					if !send(e) {
						return
					}
				}
			}
			if rerr != nil {
				break
			}
		}
		if e := p.flush(); e != nil && e.EpochMS >= sinceMS {
			send(e)
		}
		return
	}

	var f *os.File
	var offset int64
	if of, err := os.Open(logPath); err == nil {
		end, _ := of.Seek(0, io.SeekEnd)
		f = of
		offset = end
	} else {
		nf, werr := waitOpen(ctx, logPath)
		if werr != nil {
			return
		}
		f = nf
		offset = 0
	}
	defer func() { f.Close() }()

	var buf []byte
	readBuf := make([]byte, 64*1024)
	lastData := time.Now()
	flushed := false

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if e := p.flush(); e != nil {
				e.Seq = nextSeq()
				out <- e
			}
			return
		case <-ticker.C:
		}

		if info, statErr := os.Stat(logPath); statErr != nil || info.Size() < offset {
			if e := p.flush(); e != nil {
				if !send(e) {
					return
				}
			}
			f.Close()
			nf, werr := waitOpen(ctx, logPath)
			if werr != nil {
				return
			}
			f = nf
			offset = 0
			buf = buf[:0]
			p.reset()
			lastData = time.Now()
			flushed = false
		}

		for {
			n, rerr := f.Read(readBuf)
			if n > 0 {
				offset += int64(n)
				buf = append(buf, readBuf[:n]...)
			}
			if rerr != nil || n == 0 {
				break
			}
		}

		if last := bytes.LastIndexByte(buf, '\n'); last >= 0 {
			for _, lb := range bytes.Split(buf[:last], []byte{'\n'}) {
				if e := p.feed(string(lb)); e != nil {
					if !send(e) {
						return
					}
				}
			}
			rem := buf[last+1:]
			nb := make([]byte, len(rem))
			copy(nb, rem)
			buf = nb
			lastData = time.Now()
			flushed = false
		}

		if !flushed && time.Since(lastData) >= 500*time.Millisecond {
			if e := p.flush(); e != nil {
				if !send(e) {
					return
				}
			}
			flushed = true
		}
	}
}

func waitOpen(ctx context.Context, path string) (*os.File, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if f, err := os.Open(path); err == nil {
			return f, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
