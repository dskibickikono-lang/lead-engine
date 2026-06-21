// Package run sequences the pipeline stages with per-stage status
// recording. The pipeline degrades rather than dies: a failing stage is
// recorded and the run continues, finishing with a non-zero error so cron
// alerts — but later stages still execute on whatever data exists.
package run

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/hrkono/lead-engine/internal/store"
)

type Stage struct {
	Name string
	Fn   func(ctx context.Context, runID int64) error
}

type Runner struct {
	Store  *store.Store
	Stages []Stage
	Resume bool
}

func (r *Runner) Run(ctx context.Context) error {
	var skipDone map[string]bool
	if r.Resume {
		_, done, ok, err := r.Store.LastFailedRun()
		if err != nil {
			return err
		}
		if ok {
			skipDone = done
		}
	}
	runID, err := r.Store.StartRun()
	if err != nil {
		return err
	}
	var failures []string
	for _, stage := range r.Stages {
		if skipDone[stage.Name] {
			recordStage(r.Store, runID, stage.Name, "ok", "skipped (resume)")
			continue
		}
		log.Printf("stage %s: start", stage.Name)
		if err := stage.Fn(ctx, runID); err != nil {
			log.Printf("stage %s: FAILED: %v", stage.Name, err)
			recordStage(r.Store, runID, stage.Name, "failed", err.Error())
			failures = append(failures, fmt.Sprintf("%s: %v", stage.Name, err))
			continue
		}
		recordStage(r.Store, runID, stage.Name, "ok", "")
		log.Printf("stage %s: ok", stage.Name)
	}
	if len(failures) > 0 {
		if err := r.Store.FinishRun(runID, "failed"); err != nil {
			// A lost 'failed' status leaves the run 'running', so --resume can't
			// find it. Surface it rather than discarding.
			log.Printf("run %d: recording failed status: %v", runID, err)
		}
		return fmt.Errorf("run %d: %d stage(s) failed: %v", runID, len(failures), failures)
	}
	return r.Store.FinishRun(runID, "ok")
}

// recordStage persists a stage's status, logging (rather than discarding) a
// write error: a lost run_stages row desyncs --resume and would otherwise be
// silent.
func recordStage(st *store.Store, runID int64, name, status, detail string) {
	if err := st.RecordStage(runID, name, status, detail); err != nil {
		log.Printf("stage %s: recording %q status: %v", name, status, err)
	}
}

// ScraperStage runs an external scraper command and verifies its export
// file exists afterwards. cmd[0] is the binary, the rest are args.
// Note: this execs the binary directly (no shell) — wrapper scripts need
// a shebang and the execute bit.
func ScraperStage(name string, cmd []string, exportPath string) Stage {
	return Stage{Name: name, Fn: func(ctx context.Context, _ int64) error {
		if len(cmd) == 0 {
			return fmt.Errorf("%s: no command configured", name)
		}
		c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w (output: %.500s)", name, err, string(out))
		}
		if _, err := os.Stat(exportPath); err != nil {
			return fmt.Errorf("%s: export file missing: %w", name, err)
		}
		return nil
	}}
}
