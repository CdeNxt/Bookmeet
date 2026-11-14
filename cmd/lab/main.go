// Command lab is a local test bench for the personal booking assistant. It is a development
// tool: it is never built into the app image and nothing in the app depends on it.
//
// What it does:
//
//   - builds and runs the real Bookmeet binary from this checkout against a throwaway
//     database, so the prompts, tools and guardrails under test are the real ones;
//   - stands in for Telegram: the app's bot calls come here (TELEGRAM_API_BASE_URL) and the
//     lab delivers messages and button taps to the app's real webhook, with the real secret;
//   - sits between the app and the AI model and records every request and reply, so what the
//     model was told and what it said can be read afterwards;
//   - sets up a known world through the app's own API (owner, meeting types, hours, a few
//     bookings and links belonging to other people);
//   - runs scripted conversations against all of that and checks facts about the outcome.
//
// Usage, from the repository root:
//
//	go run ./cmd/lab                 start the lab (http://localhost:3066)
//	go run ./cmd/lab run [-n 3] [name ...]   run scenarios against a running lab
//
// Everything it writes lives in .lab/ (ignored by git): the database, the model details
// entered on the lab page, scenario files and run transcripts.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	labAddr = "127.0.0.1:3066"
	labURL  = "http://127.0.0.1:3066"
	appPort = "3067"
	appURL  = "http://localhost:3067"
)

func main() {
	log.SetFlags(log.Ltime)
	root, err := repoRoot()
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "run" {
		os.Exit(runClient(os.Args[2:]))
	}

	dir := filepath.Join(root, ".lab")
	for _, d := range []string{dir, filepath.Join(dir, "scenarios"), filepath.Join(dir, "runs"), filepath.Join(dir, "data")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			log.Fatal(err)
		}
	}
	lab := newLab(root, dir)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: labAddr, Handler: lab.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("lab: %v (is another lab running?)", err)
		}
	}()
	if err := lab.boot(ctx); err != nil {
		lab.stopApp()
		log.Fatalf("lab: %v", err)
	}
	log.Printf("lab ready: %s   (the app itself: %s/admin, sign-in details on the lab page)", "http://localhost:3066", appURL)
	<-ctx.Done()
	lab.stopApp()
	_ = srv.Close()
}

// runClient asks a running lab to run scenarios and prints its report as it arrives.
func runClient(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	n := fs.Int("n", 1, "times to run each scenario")
	_ = fs.Parse(args)
	q := url.Values{"n": {fmt.Sprint(*n)}}
	for _, name := range fs.Args() {
		q.Add("only", name)
	}
	resp, err := http.Post(labURL+"/lab/api/run?"+q.Encode(), "text/plain", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "The lab is not running. Start it with: go run ./cmd/lab")
		return 2
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.TeeReader(resp.Body, os.Stdout))
	if resp.StatusCode != http.StatusOK || strings.Contains(string(out), "\nFAILED") {
		return 1
	}
	return 0
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "cmd", "calnode")); err == nil {
				return d, nil
			}
		}
		if d == filepath.Dir(d) {
			return "", fmt.Errorf("run the lab from inside the Bookmeet checkout")
		}
	}
}
