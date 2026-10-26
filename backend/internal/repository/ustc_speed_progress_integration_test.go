//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUSTCSpeedProgressPersistenceAndLeaseOwnership(t *testing.T) {
	ctx := context.Background()
	repo := NewUSTCSpeedRepository(integrationDB)
	task := service.DefaultUSTCSpeedTask()
	task.Name = "progress-" + uuid.NewString()
	require.NoError(t, repo.SaveTask(ctx, &task))
	t.Cleanup(func() { require.NoError(t, repo.DeleteTask(context.Background(), task.ID)) })
	claimed, err := repo.ClaimTask(ctx, task.ID, "owner", time.Minute, true, task.Revision)
	require.NoError(t, err)
	require.True(t, claimed)
	got, err := repo.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "preparing", got.Progress.State)
	require.NotNil(t, got.LeaseUntil)
	progress := &service.USTCSpeedTaskProgress{RunID: "owner", State: "running", Total: 3, Completed: 1, Successful: 1, StartedAt: time.Now(), UpdatedAt: time.Now(), Active: []service.USTCSpeedActiveProbe{{AccountID: 7, Model: "model", Phase: "requesting", StartedAt: time.Now()}}}
	require.NoError(t, repo.UpdateTaskProgress(ctx, task.ID, "wrong-owner", progress))
	got, err = repo.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Zero(t, got.Progress.Completed)
	require.NoError(t, repo.UpdateTaskProgress(ctx, task.ID, "owner", progress))
	// Editing a configuration must neither erase nor accept forged progress.
	task.Progress = &service.USTCSpeedTaskProgress{RunID: "forged", Completed: 999}
	require.NoError(t, repo.SaveTask(ctx, &task))
	require.Equal(t, 1, task.Progress.Completed)
	require.Equal(t, "owner", task.Progress.RunID)
	require.Len(t, task.Progress.Active, 1)
	require.NoError(t, repo.FinishTask(ctx, task.ID, "owner", time.Now(), "completed", ""))
	got, err = repo.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", got.Progress.State, "an edited run cannot report success")
	require.Equal(t, "task_changed", got.Progress.Reason)
	require.Empty(t, got.Progress.Active)
	require.NotNil(t, got.Progress.FinishedAt)
	require.Nil(t, got.LeaseUntil)
	require.NoError(t, repo.UpdateTaskProgress(ctx, task.ID, "owner", progress))
	got, err = repo.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", got.Progress.State, "late heartbeats cannot resurrect finished runs")
	claimed, err = repo.ClaimTask(ctx, task.ID, "new-owner", time.Minute, true, task.Revision)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repo.UpdateTaskProgress(ctx, task.ID, "owner", progress))
	got, err = repo.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "new-owner", got.Progress.RunID)
	require.Zero(t, got.Progress.Completed)
}
