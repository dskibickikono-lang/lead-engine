package run

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hrkono/lead-engine/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "leads.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestRunnerRecordsStagesAndResumes(t *testing.T) {
	st := testStore(t)
	calls := map[string]int{}
	mk := func(name string, fail bool) Stage {
		return Stage{Name: name, Fn: func(ctx context.Context) error {
			calls[name]++
			if fail {
				return errors.New("boom")
			}
			return nil
		}}
	}

	r := &Runner{Store: st, Stages: []Stage{mk("a", false), mk("b", true), mk("c", false)}}
	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected failure from stage b")
	}
	if calls["a"] != 1 || calls["b"] != 1 || calls["c"] != 1 {
		t.Errorf("calls = %v (failures must not stop later stages)", calls)
	}

	// Resume: a and c completed, only b re-runs.
	r2 := &Runner{Store: st, Stages: []Stage{mk("a", false), mk("b", false), mk("c", false)}, Resume: true}
	r2.Stages[1].Fn = func(ctx context.Context) error { calls["b"]++; return nil }
	if err := r2.Run(context.Background()); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if calls["a"] != 1 || calls["b"] != 2 || calls["c"] != 1 {
		t.Errorf("after resume calls = %v", calls)
	}
}

func TestResumeAfterSuccessRunsEverything(t *testing.T) {
	st := testStore(t)
	calls := map[string]int{}
	mk := func(name string, fail bool) Stage {
		return Stage{Name: name, Fn: func(ctx context.Context) error {
			calls[name]++
			if fail {
				return errors.New("boom")
			}
			return nil
		}}
	}

	// Run 1 fails on b; run 2 (no resume) succeeds fully.
	r := &Runner{Store: st, Stages: []Stage{mk("a", false), mk("b", true), mk("c", false)}}
	if err := r.Run(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	r2 := &Runner{Store: st, Stages: []Stage{mk("a", false), mk("b", false), mk("c", false)}}
	if err := r2.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Resume after a successful latest run must NOT skip anything from the
	// older failed run.
	calls = map[string]int{}
	r3 := &Runner{Store: st, Stages: []Stage{mk("a", false), mk("b", false), mk("c", false)}, Resume: true}
	if err := r3.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls["a"] != 1 || calls["b"] != 1 || calls["c"] != 1 {
		t.Errorf("resume after success skipped stages: %v", calls)
	}
}
