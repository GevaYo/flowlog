package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type osSink struct {
	baseURL     string
	indexPrefix string // docs go to <prefix>-YYYY.MM.DD (daily indices, cheap to rotate)
	instanceID  string
	ch          chan *Entry
	client      *http.Client
	dropOnce    sync.Once
	stopOnce    sync.Once
	quit        chan struct{}
	done        chan struct{}
	failWarned  bool
}

type osDoc struct {
	Time        int64  `json:"time"`
	Timestamp   int64  `json:"timestamp"`
	TimestampNS int64  `json:"timestamp_ns"`
	Service     string `json:"service"`
	Level       string `json:"level"`
	OperationID string `json:"operation_id"`
	Message     string `json:"message"`
	Params      string `json:"params"`
	InstanceID  string `json:"instance_id"`
}

func newOSSink(baseURL string, index string) *osSink {
	host, _ := os.Hostname()
	return &osSink{baseURL: baseURL, indexPrefix: index, instanceID: host, ch: make(chan *Entry, 4096), client: &http.Client{Timeout: 5 * time.Second}, quit: make(chan struct{}), done: make(chan struct{})}
}

// docID derives a stable document id from the entry content, so re-shipping the
// same time window overwrites documents instead of duplicating them.
func docID(e *Entry) string {
	h := sha1.New()
	io.WriteString(h, e.Service)
	io.WriteString(h, "\x00")
	io.WriteString(h, strconv.FormatInt(e.EpochMS, 10))
	io.WriteString(h, "\x00")
	for _, l := range e.Lines {
		io.WriteString(h, l)
		io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *osSink) add(e *Entry) {
	select {
	case s.ch <- e:
	default:
		s.dropOnce.Do(func() { fmt.Fprintln(os.Stderr, "flowlog: opensearch sink buffer full, dropping entries") })
	}
}

// cleanup deletes daily indices older than retainDays (0 disables). Runs once
// at startup; with daily use that is enough to keep the store bounded.
func (s *osSink) cleanup(retainDays int) {
	if retainDays <= 0 {
		return
	}
	resp, err := s.client.Get(s.baseURL + "/_cat/indices/" + s.indexPrefix + "-*?format=json&h=index")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var indices []struct {
		Index string `json:"index"`
	}
	if json.NewDecoder(resp.Body).Decode(&indices) != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -retainDays)
	for _, idx := range indices {
		day, err := time.ParseInLocation("2006.01.02", strings.TrimPrefix(idx.Index, s.indexPrefix+"-"), time.Local)
		if err != nil || !day.Before(cutoff) {
			continue
		}
		if req, err := http.NewRequest(http.MethodDelete, s.baseURL+"/"+idx.Index, nil); err == nil {
			if resp, err := s.client.Do(req); err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}
	}
}

func (s *osSink) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	batch := make([]*Entry, 0, 500)
	for {
		select {
		case <-ctx.Done():
			s.flush(batch)
			close(s.done)
			return
		case <-s.quit:
			s.flush(batch)
			close(s.done)
			return
		case e := <-s.ch:
			batch = append(batch, e)
			if len(batch) >= 500 {
				s.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				s.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

func (s *osSink) drain() {
	s.stopOnce.Do(func() { close(s.quit) })
	<-s.done
	batch := make([]*Entry, 0, 500)
	for {
		select {
		case e := <-s.ch:
			batch = append(batch, e)
			if len(batch) >= 500 {
				s.flush(batch)
				batch = batch[:0]
			}
		default:
			s.flush(batch)
			return
		}
	}
}

func (s *osSink) flush(batch []*Entry) {
	if len(batch) == 0 {
		return
	}
	var buf bytes.Buffer
	for _, e := range batch {
		day := time.UnixMilli(e.EpochMS).Format("2006.01.02")
		meta, _ := json.Marshal(map[string]map[string]string{"index": {"_index": s.indexPrefix + "-" + day, "_id": docID(e)}})
		buf.Write(meta)
		buf.WriteByte('\n')
		// timestamp_ns: the log only carries millisecond precision, so sub-ms digits are synthesized from Seq to keep same-ms entries ordered and unique (like the QA index's ns field).
		doc := osDoc{Time: e.EpochMS, Timestamp: e.EpochMS, TimestampNS: e.EpochMS*1_000_000 + int64(e.Seq%1_000_000), Service: e.Service, Level: e.Level, OperationID: e.OperationID, Message: e.Message, Params: strings.Join(e.Lines[1:], "\n"), InstanceID: s.instanceID}
		docBytes, err := json.Marshal(doc)
		if err != nil {
			continue
		}
		buf.Write(docBytes)
		buf.WriteByte('\n')
	}
	body := buf.Bytes()
	backoffs := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	for attempt := 0; ; attempt++ {
		err := s.post(body)
		if err == nil {
			s.failWarned = false
			return
		}
		if attempt >= len(backoffs) {
			if !s.failWarned {
				fmt.Fprintf(os.Stderr, "flowlog: opensearch _bulk unreachable, dropping %d entries: %v\n", len(batch), err)
				s.failWarned = true
			}
			return
		}
		time.Sleep(backoffs[attempt])
	}
}

func (s *osSink) post(body []byte) error {
	req, err := http.NewRequest(http.MethodPost, s.baseURL+"/_bulk", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("opensearch returned status %d", resp.StatusCode)
	}
	return nil
}
