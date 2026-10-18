package home

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ScheduledBackupOptions struct {
	Data, Runtime, Address, ServerID string
	Clock                            func() time.Time
	Interval                         time.Duration
	Free                             func(string) (uint64, error)
	Size                             func(context.Context, string) (uint64, error)
	Export                           func(context.Context, string, string, string, string) error
}

func StartScheduledBackups(parent context.Context, options ScheduledBackupOptions) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	if options.Interval <= 0 {
		options.Interval = time.Minute
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(options.Interval)
		defer ticker.Stop()
		for {
			_ = RunScheduledBackupCycle(ctx, options)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func RunScheduledBackupCycle(ctx context.Context, options ScheduledBackupOptions) error {
	root := filepath.Dir(options.Data)
	var policy BackupPolicy
	if err := readBackupJSON(filepath.Join(root, "backup-policy.json"), &policy); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return writeBackupJSON(filepath.Join(root, "backup-status.json"), BackupStatus{Format: 1, Code: "invalid_policy"})
	}
	if err := policy.Validate(); err != nil {
		return writeBackupJSON(filepath.Join(root, "backup-status.json"), BackupStatus{Format: 1, Code: "invalid_policy"})
	}
	var status BackupStatus
	if err := readBackupJSON(filepath.Join(root, "backup-status.json"), &status); err != nil || status.Format != 1 {
		status = BackupStatus{}
	}
	if status.ServerID != options.ServerID {
		status = BackupStatus{}
	}
	status.Format, status.ServerID = 1, options.ServerID
	if !policy.Enabled {
		status.Code = "disabled"
		status.NextAt, status.Policy = 0, ""
		return writeBackupJSON(filepath.Join(root, "backup-status.json"), status)
	}
	options = defaultScheduledOptions(options)
	encoded, _ := json.Marshal(policy)
	hash := fmt.Sprintf("%x", sha256.Sum256(encoded))
	now := options.Clock()
	if status.Policy == hash && status.NextAt > now.UnixMilli() {
		return nil
	}
	job, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	return runScheduledJob(job, options, policy, status, hash, now)
}

func defaultScheduledOptions(options ScheduledBackupOptions) ScheduledBackupOptions {
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Free == nil {
		options.Free = scheduledFreeBytes
	}
	if options.Size == nil {
		options.Size = scheduledDatabaseBytes
	}
	if options.Export == nil {
		options.Export = exportScheduled
	}
	return options
}

func runScheduledJob(ctx context.Context, options ScheduledBackupOptions, policy BackupPolicy, status BackupStatus, hash string, now time.Time) error {
	root := filepath.Dir(options.Data)
	stateFile := filepath.Join(root, "backup-status.json")
	status.Policy, status.LastAttemptAt = hash, now.UnixMilli()
	status.NextAt = now.Add(5 * time.Minute).UnixMilli()
	fail := func(code string) error { status.Code = code; return writeBackupJSON(stateFile, status) }
	folder, err := scheduledFolder(root)
	if err != nil {
		return fail("failed")
	}
	items, protected, err := listScheduled(ctx, folder, options.ServerID)
	if err != nil {
		return fail("failed")
	}
	status.Protected = protected
	items, err = pruneScheduled(ctx, folder, options.ServerID, policy, now, items)
	if err != nil {
		return fail("failed")
	}
	status.Count, status.Bytes = len(items), 0
	for _, item := range items {
		status.Bytes += item.Bytes
	}
	status.LastSuccessAt = 0
	if len(items) > 0 {
		status.LastSuccessAt = items[0].CreatedAt
	}
	status.NextAt = status.LastSuccessAt + int64(policy.IntervalHours)*int64(time.Hour/time.Millisecond)
	if status.NextAt <= now.UnixMilli() {
		status.Code = "running"
		if err := writeBackupJSON(stateFile, status); err != nil {
			return err
		}
		if err := requireScheduledSpace(ctx, options, folder); err != nil {
			status.NextAt = now.Add(5 * time.Minute).UnixMilli()
			return fail("space_low")
		}
		item, err := publishScheduled(ctx, folder, options.ServerID, now, func(destination string) error {
			return options.Export(ctx, options.Runtime, options.Data, options.Address, destination)
		})
		if err != nil {
			status.NextAt = now.Add(5 * time.Minute).UnixMilli()
			return fail("failed")
		}
		items = append([]scheduledArchive{item}, items...)
		status.LastSuccessAt = item.CreatedAt
		status.NextAt = now.Add(time.Duration(policy.IntervalHours) * time.Hour).UnixMilli()
		items, err = pruneScheduled(ctx, folder, options.ServerID, policy, now, items)
		if err != nil {
			return fail("failed")
		}
	}
	status.Code, status.Count, status.Bytes = "ok", len(items), 0
	for _, item := range items {
		status.Bytes += item.Bytes
	}
	status.Limited = policy.MaxBytes > 0 && status.Bytes > policy.MaxBytes
	return writeBackupJSON(stateFile, status)
}

func requireScheduledSpace(ctx context.Context, options ScheduledBackupOptions, folder string) error {
	size, err := options.Size(ctx, options.Address)
	if err != nil {
		return err
	}
	free, err := options.Free(folder)
	if err != nil {
		return err
	}
	if size > (^uint64(0)-(256<<20))/2 || free < size*2+(256<<20) {
		return fmt.Errorf("Недостаточно места для снимка")
	}
	return nil
}
