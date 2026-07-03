package main

import (
	"flag"
	"slices"
	"testing"
	"time"
)

// A lone "-" is a positional arg the flag package refuses to consume; the
// interleave loop must swallow it too or it spins forever at 100% CPU.
func TestParseMixedLoneDash(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	typ := fs.String("type", "", "")

	done := make(chan []string, 1)
	go func() { done <- parseMixed(fs, []string{"fix", "-", "thing", "--type", "art"}) }()

	select {
	case got := <-done:
		if want := []string{"fix", "-", "thing"}; !slices.Equal(got, want) {
			t.Fatalf("positional = %v, want %v", got, want)
		}
		if *typ != "art" {
			t.Fatalf("interleaved flag lost: type=%q", *typ)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parseMixed spun forever on a lone dash")
	}
}

// The original motivation for parseMixed: flags AFTER positionals must not
// be silently eaten as positionals.
func TestParseMixedInterleaves(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	typ := fs.String("type", "", "")
	first := fs.Bool("first", false, "")

	got := parseMixed(fs, []string{"paint the sky", "--type", "art", "--first"})
	if want := []string{"paint the sky"}; !slices.Equal(got, want) {
		t.Fatalf("positional = %v, want %v", got, want)
	}
	if *typ != "art" || !*first {
		t.Fatalf("flags lost: type=%q first=%v", *typ, *first)
	}
}
