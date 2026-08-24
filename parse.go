package main

import (
	"regexp"
	"strconv"
	"strings"
)

var headerRe = regexp.MustCompile(`^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})? \| (error|warn|info|verbose|silly|debug) \| (\d+) \| `)

type parser struct {
	alias string
	cur   *Entry
	// metadata of the most recently flushed entry, so continuation lines that
	// arrive after an idle flush are attached instead of dropped
	lastOp    string
	lastLevel string
	lastEpoch int64
}

func newParser(serviceAlias string) *parser { return &parser{alias: serviceAlias} }

func (p *parser) feed(line string) *Entry {
	m := headerRe.FindStringSubmatch(line)
	if m == nil {
		if p.cur != nil {
			p.cur.Lines = append(p.cur.Lines, line)
			return nil
		}
		if p.lastEpoch != 0 {
			// continuation of an entry that was already flushed mid-write:
			// resume as a follow-up entry with the same metadata
			p.cur = &Entry{Service: p.alias, OperationID: p.lastOp, Level: p.lastLevel, EpochMS: p.lastEpoch, Message: line, Lines: []string{line}}
		}
		return nil
	}
	prev := p.cur
	epochMS, _ := strconv.ParseInt(m[3], 10, 64)
	message := ""
	if parts := strings.SplitN(line, " | ", 6); len(parts) >= 5 {
		message = parts[4]
	}
	p.cur = &Entry{Service: p.alias, OperationID: m[1], Level: m[2], EpochMS: epochMS, Message: message, Lines: []string{line}}
	return prev
}

// reset drops the resume metadata; called on log rotation so a partial first
// line in the fresh file is not attributed to a pre-rotation entry.
func (p *parser) reset() { p.lastOp, p.lastLevel, p.lastEpoch = "", "", 0 }

func (p *parser) flush() *Entry {
	e := p.cur
	if e != nil {
		p.lastOp, p.lastLevel, p.lastEpoch = e.OperationID, e.Level, e.EpochMS
	}
	p.cur = nil
	return e
}
