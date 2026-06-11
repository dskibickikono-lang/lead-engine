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
	Fn   func(ctx context.Context) error
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
			r.Store.RecordStage(runID, stage.Name, "ok", "skipped (resume)")
			continue
		}
		log.Printf("stage %s: start", stage.Name)
		if err := stage.Fn(ctx); err != nil {
			log.Printf("stage %s: FAILED: %v", stage.Name, err)
			r.Store.RecordStage(runID, stage.Name, "failed", err.Error())
			failures = append(failures, fmt.Sprintf("%s: %v", stage.Name, err))
			continue
		}
		r.Store.RecordStage(runID, stage.Name, "ok", "")
		log.Printf("stage %s: ok", stage.Name)
	}
	if len(failures) > 0 {
		r.Store.FinishRun(runID, "failed")
		return fmt.Errorf("run %d: %d stage(s) failed: %v", runID, len(failures), failures)
	}
	return r.Store.FinishRun(runID, "ok")
}

// ScraperStage runs an external scraper command and verifies its export
// file exists afterwards. cmd[0] is the binary, the rest are args.
// Note: this execs the binary directly (no shell) — wrapper scripts need
// a shebang and the execute bit.
func ScraperStage(name string, cmd []string, exportPath string) Stage {
	return Stage{Name: name, Fn: func(ctx context.Context) error {
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
