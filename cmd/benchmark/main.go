package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/jerkeyray/watchkeeper/internal/benchmark"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fatal("usage: benchmark run|resume|summarize -config benchmarks/email.json -output results/email")
	}
	mode := os.Args[1]
	if mode == "source-hash" {
		fmt.Println(benchmark.SourceHash())
		return
	}
	if mode == "worker" || mode == "coordinator" {
		benchmark.ChildExit(mode)
		return
	}
	flags := flag.NewFlagSet(mode, flag.ExitOnError)
	config := flags.String("config", "benchmarks/email.json", "versioned configuration")
	output := flags.String("output", "results/email", "suite output directory")
	flags.Parse(os.Args[2:])
	if mode == "summarize" {
		if err := benchmark.Summarize(*output); err != nil {
			fatal("%v", err)
		}
		return
	}
	if mode != "run" && mode != "resume" {
		fatal("unknown mode %q", mode)
	}
	c, err := benchmark.LoadConfig(*config)
	if err != nil {
		fatal("%v", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err = benchmark.Run(ctx, c, benchmark.EnvironmentFromOS(), *output, mode == "resume"); err != nil {
		fatal("%v", err)
	}
}
func fatal(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }
