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
	// seenRunIDs collects the runID passed to each stage invocation.
	var seenRunIDs []int64
	mk := func(name string, fail bool) Stage {
		return Stage{Name: name, Fn: func(ctx context.Context, runID int64) error {
			calls[name]++
			seenRunIDs = append(seenRunIDs, runID)
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
	// All three stages of run 1 must receive a non-zero, identical runID.
	if len(seenRunIDs) != 3 {
		t.Fatalf("expected 3 runID observations, got %d", len(seenRunIDs))
	}
	if seenRunIDs[0] == 0 {
		t.Errorf("runID passed to stages must be non-zero, got %d", seenRunIDs[0])
	}
	if seenRunIDs[0] != seenRunIDs[1] || seenRunIDs[1] != seenRunIDs[2] {
		t.Errorf("runID must be equal across stages of one run: %v", seenRunIDs)
	}

	// Resume: a and c completed, only b re-runs.
	r2 := &Runner{Store: st, Stages: []Stage{mk("a", false), mk("b", false), mk("c", false)}, Resume: true}
	r2.Stages[1].Fn = func(ctx context.Context, runID int64) error { calls["b"]++; return nil }
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
		return Stage{Name: name, Fn: func(ctx context.Context, _ int64) error {
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
