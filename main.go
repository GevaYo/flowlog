package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type cliConfig struct {
	follow     bool
	op         string
	levels     map[string]bool
	profile    string
	services   []string
	ship       bool
	osURL      string
	index      string
	retainDays int
	noColor    bool
	configPath string
	sinceMS    int64
}

func main() {
	// Subcommands are matched before flag parsing so they get their own flag set.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "doctor":
			os.Exit(runChecks(os.Args[2:], false))
		case "setup":
			os.Exit(runChecks(os.Args[2:], true))
		case "start":
			os.Exit(runStart(os.Args[2:]))
		case "attach":
			os.Exit(runAttach(os.Args[2:]))
		case "profiles":
			os.Exit(runProfiles(os.Args[2:]))
		case "completion":
			os.Exit(runCompletion(os.Args[2:]))
		}
	}

	cfg := parseFlags()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	services, err := loadServices(cfg.configPath, cfg.profile, cfg.services)
	if err != nil {
		fatalf("%v", err)
	}
	if len(services) == 0 {
		fatalf("no services matched profile %q / filter %v", cfg.profile, cfg.services)
	}

	color := !cfg.noColor && isTerminal(os.Stdout)
	r := newRenderer(os.Stdout, services, color)
	defer r.flush()

	var sink *osSink
	if cfg.ship {
		sink = newOSSink(cfg.osURL, cfg.index)
		go func() {
			sink.cleanup(cfg.retainDays)
			sink.run(ctx)
		}()
	}

	r.banner(bannerMode(cfg), services, bannerFilters(cfg))

	pass := makeFilter(cfg.levels, cfg.op)
	emit := func(e *Entry) {
		if !pass(e) {
			return
		}
		r.print(e)
		if sink != nil {
			sink.add(e)
		}
	}

	entries := startTailers(ctx, services, cfg.follow, cfg.sinceMS)
	if cfg.follow {
		mergeLive(ctx, entries, 250*time.Millisecond, emit)
	} else {
		all := collectEntries(entries)
		sortEntries(all)
		for _, e := range all {
			emit(e)
		}
	}

	if sink != nil {
		sink.drain()
	}
}

func parseFlags() cliConfig {
	follow := flag.Bool("f", false, "follow live from EOF; omit for a post-hoc scan")
	op := flag.String("op", "", "keep only entries with this operation id")
	since := flag.Duration("since", time.Hour, "post-hoc scan window before now")
	level := flag.String("level", "", "comma-separated levels to include")
	profile := flag.String("profile", "", "service profile from dev-services.json")
	services := flag.String("services", "", "comma-separated service names or aliases")
	ship := flag.Bool("ship", false, "ship entries to OpenSearch")
	osURL := flag.String("os-url", "http://localhost:9200", "OpenSearch base URL")
	index := flag.String("index", "flowlog", "OpenSearch index prefix; docs are written to <prefix>-YYYY.MM.DD daily indices")
	retain := flag.Int("retain", 7, "with --ship: delete daily indices older than this many days (0 = keep forever)")
	noColor := flag.Bool("no-color", false, "disable ANSI color")
	flag.Parse()

	configPath, err := defaultConfigPath()
	if err != nil {
		fatalf("cannot resolve home directory: %v", err)
	}

	cfg := cliConfig{
		follow:     *follow,
		op:         *op,
		levels:     levelSet(*level),
		profile:    *profile,
		services:   splitCSV(*services),
		ship:       *ship,
		osURL:      *osURL,
		index:      *index,
		retainDays: *retain,
		noColor:    *noColor,
		configPath: configPath,
	}
	if !cfg.follow {
		cfg.sinceMS = time.Now().Add(-*since).UnixMilli()
	}
	return cfg
}

func bannerMode(cfg cliConfig) string {
	if cfg.follow {
		return "following"
	}
	return "scanning since " + time.UnixMilli(cfg.sinceMS).Format("15:04:05") + " in"
}

func bannerFilters(cfg cliConfig) string {
	var parts []string
	if cfg.profile != "" {
		parts = append(parts, "profile "+cfg.profile)
	}
	if len(cfg.levels) > 0 {
		var lv []string
		for l := range cfg.levels {
			lv = append(lv, l)
		}
		parts = append(parts, "levels "+strings.Join(lv, ","))
	}
	if cfg.op != "" {
		parts = append(parts, "op "+cfg.op)
	}
	if cfg.ship {
		parts = append(parts, "shipping to "+cfg.osURL+"/"+cfg.index+"-YYYY.MM.DD")
	}
	return strings.Join(parts, ", ")
}

func startTailers(ctx context.Context, services []Service, follow bool, sinceMS int64) <-chan *Entry {
	entries := make(chan *Entry, 256)
	var wg sync.WaitGroup
	for _, svc := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tailService(ctx, svc, follow, sinceMS, entries)
		}()
	}
	go func() {
		wg.Wait()
		close(entries)
	}()
	return entries
}

func collectEntries(in <-chan *Entry) []*Entry {
	var all []*Entry
	for e := range in {
		all = append(all, e)
	}
	return all
}

func makeFilter(levels map[string]bool, op string) func(*Entry) bool {
	return func(e *Entry) bool {
		if len(levels) > 0 && !levels[e.Level] {
			return false
		}
		return op == "" || e.OperationID == op
	}
}

func levelSet(csv string) map[string]bool {
	set := make(map[string]bool)
	for _, l := range splitCSV(csv) {
		set[strings.ToLower(l)] = true
	}
	return set
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "flowlog: "+format+"\n", args...)
	os.Exit(1)
}
