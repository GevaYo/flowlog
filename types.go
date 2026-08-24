package main

import "sync/atomic"

// Service is one entry from ~/dev-services.json resolved to an absolute repo path.
type Service struct {
	Name  string // path under services_root, e.g. "integrations/acme-gateway"
	Alias string // display name, e.g. "Acme"; falls back to Name when absent
	Path  string // absolute repo dir; the log file is Path + "/rapyd.log"
}

// Entry is one logical log entry: a header line plus its continuation lines.
type Entry struct {
	Service     string   // Service.Alias
	OperationID string   // "" when the header carried no operation id
	Level       string   // error|warn|info|verbose|silly|debug
	EpochMS     int64    // header field 3
	Message     string   // header field 5
	Lines       []string // raw lines; Lines[0] is the header line
	Seq         uint64   // arrival order from nextSeq(); tie-break after (EpochMS, Service)
}

var seqCounter atomic.Uint64

func nextSeq() uint64 { return seqCounter.Add(1) }

// Contracts implemented across the package. Signatures are pinned - do not deviate.
//
// config.go
//   func loadServices(configPath string, profile string, only []string) ([]Service, error)
//     - expands $HOME in services_root; profile "" = all services; only (aliases or
//       names, case-insensitive) further filters; unknown profile is an error.
//
// parse.go
//   var headerRe *regexp.Regexp
//   func newParser(serviceAlias string) *parser
//   func (p *parser) feed(line string) *Entry // returns the COMPLETED previous entry when line starts a new one, else nil
//   func (p *parser) flush() *Entry           // returns the in-progress entry (EOF/idle), nil if none
//     - header: "{uuid or empty} | {level} | {epoch_ms} | {date} | {message} | {params}"
//     - non-header lines are continuations appended to the current entry
//
// tail.go
//   func tailService(ctx context.Context, svc Service, follow bool, sinceMS int64, out chan<- *Entry)
//     - follow: start at EOF, poll 100ms, reopen at offset 0 when the file shrinks
//       (winston rotation, maxsize 5MB tailable), wait for a missing file to appear,
//       idle-flush the in-progress entry after 500ms, return on ctx.Done
//     - scan (follow=false): read the whole file, drop entries with EpochMS < sinceMS, return at EOF
//     - only parse up to the last '\n'; keep the partial tail for the next read
//     - sets Entry.Seq via nextSeq() in arrival order
//
// merge.go
//   func mergeLive(ctx context.Context, in <-chan *Entry, holdback time.Duration, emit func(*Entry))
//     - min-heap ordered by (EpochMS, Service, Seq); 50ms ticker emits entries older
//       than holdback; drains fully when in closes or ctx cancels
//   func sortEntries(es []*Entry) // same ordering, post-hoc mode
//
// render.go
//   func newRenderer(w io.Writer, services []Service, color bool) *renderer
//   func (r *renderer) print(e *Entry)
//     - "HH:MM:SS.mmm ALIAS level message" (local time from EpochMS); continuation
//       lines indented under a dim vertical-bar gutter; stable per-service ANSI color
//       assigned by services order; level colors: error red, warn yellow, info green,
//       verbose cyan, silly magenta
//
// sink_opensearch.go
//   func newOSSink(baseURL string, index string) *osSink
//   func (s *osSink) add(e *Entry)            // non-blocking enqueue
//   func (s *osSink) run(ctx context.Context) // flush every 1s or 500 entries via POST /_bulk (ndjson); 3 retries with backoff; warn to stderr and drop if unreachable
//   func (s *osSink) drain()                  // final synchronous flush
//     - doc fields: @timestamp (EpochMS, epoch_millis), service, level, operation_id, message, params (continuation lines joined)
//
// main.go
//   flags: -f, --op, --since (default 1h), --level (csv), --profile, --services (csv),
//          --ship, --os-url (default http://localhost:9200), --index (default "flowlog"), --no-color
//   wiring: entries chan cap 256; sync.WaitGroup over tailers; close(entries) when all
//   tailers return; live -> mergeLive(250ms holdback); post-hoc -> collect + sortEntries;
//   filters (level set, --op) applied in main before renderer/sink; color auto-off when
//   stdout is not a TTY; SIGINT via signal.NotifyContext -> tailers stop -> drain -> flush
