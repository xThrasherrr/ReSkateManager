// Command reskate-watch reports what a ReSkate release changes that the
// manager relies on, and what it adds that the manager could take up. The
// ReSkate watch workflow (.github/workflows/reskate-watch.yml) runs it for
// each new release and files the report as an issue.
//
// Nothing here is a list kept by hand. The manager's side is read from its
// own code (the settings schema, the strings it matches and sends), and the
// server's from the release and its source, so what the report asks for
// shrinks as the manager catches up.
//
//	go run ./cmd/reskate-watch -src ../ReSkate -from v1.1.8 -to v2.0.0
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	src := flag.String("src", "", "a ReSkate git checkout with the release tags, for the source checks (none: skipped)")
	from := flag.String("from", "", "the release to compare the source from")
	to := flag.String("to", "", "the release to compare the source to")
	repo := flag.String("repo", "Dingo-Shenanigans/ReSkate", "the GitHub repository ReSkate releases from")
	server := flag.Bool("server", true, "install the latest release with the manager's updater and run its server")
	root := flag.String("manager", ".", "the manager's source tree")
	out := flag.String("out", "", "write the report here (default: stdout)")
	summary := flag.String("summary", "", "write the issue's title and whether it needs work here, as JSON")
	flag.Parse()
	if *src != "" && (*from == "" || *to == "") {
		fatal("-src needs -from and -to")
	}
	if *src == "" && !*server {
		fatal("nothing to check: give -src, or leave -server on")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	r := &report{Repo: *repo, From: *from, To: *to}
	if *src != "" {
		if err := r.loadSource(*src); err != nil {
			fatal(err.Error())
		}
	}
	if *server {
		r.checkServer(ctx)
	}
	if *src != "" {
		use, err := managerStrings(*root)
		if err != nil {
			fatal(err.Error())
		}
		r.checkSource(use)
	}

	text := r.markdown()
	if *out == "" {
		fmt.Print(text)
	} else if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
		fatal(err.Error())
	}
	if *summary != "" {
		data, _ := json.Marshal(map[string]any{"tag": r.To, "title": r.title(), "open": r.needsWork()})
		if err := os.WriteFile(*summary, data, 0o644); err != nil {
			fatal(err.Error())
		}
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "reskate-watch:", msg)
	os.Exit(1)
}
