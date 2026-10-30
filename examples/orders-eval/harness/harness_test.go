package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemporalStartupStopsWaitingWhenTheScenarioIsCanceled(t *testing.T) {
	// A temporal that never listens; only scenario cancellation can end the
	// startup wait before its one-minute limit.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "temporal"), []byte("#!/bin/sh\nexec sleep 300\n"), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	p := &Platform{logDir: t.TempDir()}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, _, err := p.startTemporal(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), 10*time.Second, "startup stops waiting when the scenario ends")
	require.NoError(t, p.Close())
	select {
	case <-p.temporalDone:
	default:
		t.Fatal("Close returned before the Temporal process exited")
	}
}
