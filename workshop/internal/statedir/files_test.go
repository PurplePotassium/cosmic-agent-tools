package statedir

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// ReadProposals: a missing or empty file means "no proposals" with no error;
// a corrupt file surfaces its parse error (the worker turns it into a
// proposals.invalid event); valid entries without a title are dropped.
func TestReadProposals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProposalsFile)

	props, err := ReadProposals(dir)
	if err != nil || props != nil {
		t.Fatalf("missing file: got (%v, %v), want (nil, nil)", props, err)
	}

	if err := os.WriteFile(path, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	props, err = ReadProposals(dir)
	if err != nil || props != nil {
		t.Fatalf("empty file: got (%v, %v), want (nil, nil)", props, err)
	}

	if err := os.WriteFile(path, []byte(`{"not": "an array"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProposals(dir); err == nil {
		t.Fatal("corrupt file: want a parse error, got nil")
	}

	if err := WriteJSON(path, []domain.Proposal{{Title: "keep me"}, {Title: ""}}); err != nil {
		t.Fatal(err)
	}
	props, err = ReadProposals(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 || props[0].Title != "keep me" {
		t.Fatalf("valid file: got %+v, want the one titled entry", props)
	}
}
