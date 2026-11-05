package webupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	manager "github.com/seakee/cpa-manager-plus/apps/manager-server/pkg/embedded"
)

func executableName() string {
	if os.PathSeparator == '\\' {
		return "cliproxyapi-plus.exe"
	}
	return "cliproxyapi-plus"
}
func snapshotPath(op Operation) string {
	return filepath.Join(op.Options.operationDir(op.ID), "snapshot")
}
func snapshot(op *Operation) error {
	lock, err := manager.AcquireDataLock(op.Options.Data)
	if err != nil {
		return fmt.Errorf("database is not offline: %w", err)
	}
	defer lock.Close()
	root := snapshotPath(*op)
	if err = privateDirectory(root); err != nil {
		return err
	}
	entries := map[string]string{}
	total := int64(0)
	add := func(source, relative string) error {
		info, e := os.Lstat(source)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("snapshot cannot copy non-regular file: %s", relative)
		}
		total += info.Size()
		if total > maxSnapshot || len(entries) >= 100000 {
			return errors.New("snapshot exceeds size or file-count limit")
		}
		target := filepath.Join(root, filepath.FromSlash(relative))
		if e = privateDirectory(filepath.Dir(target)); e != nil {
			return e
		}
		if e = copyFile(source, target, maxSnapshot, 0600); e != nil {
			return e
		}
		hash, e := fileHash(target)
		if e == nil {
			entries[relative] = hash
		}
		return e
	}
	if err = filepath.WalkDir(op.Options.Data, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = noLinks(path); e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".manager.lock") {
			return nil
		}
		rel, e := filepath.Rel(op.Options.Data, path)
		if e != nil {
			return e
		}
		return add(path, "data/"+filepath.ToSlash(rel))
	}); err != nil {
		return err
	}
	if err = add(op.Options.Config, "config.yaml"); err != nil {
		return err
	}
	if err = add(op.Options.Binary, "previous-binary"); err != nil {
		return err
	}
	if entries["previous-binary"] != op.OriginalHash {
		return errors.New("installed binary changed before snapshot")
	}
	saved := *op
	saved.Snapshot = entries
	if err = saveOperation(&saved); err != nil {
		return err
	}
	*op = saved
	return verifySnapshot(*op)
}
func safeRelative(path string) bool {
	if path == "" || strings.ContainsAny(path, "\\:\x00") || strings.HasPrefix(path, "/") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}
func verifySnapshot(op Operation) error {
	if len(op.Snapshot) < 3 || len(op.Snapshot) > 100000 || op.Snapshot["previous-binary"] != op.OriginalHash {
		return errors.New("missing matching snapshot manifest")
	}
	root := snapshotPath(op)
	total := int64(0)
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = noLinks(path); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !safeRelative(key) || (key != "previous-binary" && key != "config.yaml" && !strings.HasPrefix(key, "data/")) {
			return errors.New("invalid snapshot entry")
		}
		hash, err := fileHash(path)
		if err != nil || op.Snapshot[key] != hash {
			return errors.New("snapshot inventory or checksum mismatch")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		count++
		if total > maxSnapshot {
			return errors.New("snapshot size exceeds limit")
		}
		return nil
	})
	if err == nil && count != len(op.Snapshot) {
		err = errors.New("snapshot file count mismatch")
	}
	return err
}
func restore(op *Operation, backup Operation) error {
	if err := verifySnapshot(backup); err != nil {
		return err
	}
	// Prepare restored files without disturbing the failed installation. Directory
	// rename is only used within the original data parent, never across filesystems.
	restoreID, err := newID()
	if err != nil {
		return err
	}
	staged := op.Options.Data + ".restore-" + restoreID
	if _, err := os.Lstat(staged); !errors.Is(err, os.ErrNotExist) {
		return errors.New("restore staging path already exists; inspect the journal")
	}
	if err := privateDirectory(staged); err != nil {
		return err
	}
	root := snapshotPath(backup)
	for relative := range backup.Snapshot {
		if !strings.HasPrefix(relative, "data/") {
			continue
		}
		dest := filepath.Join(staged, filepath.FromSlash(strings.TrimPrefix(relative, "data/")))
		if !inside(staged, dest) {
			return errors.New("snapshot path escapes restore directory")
		}
		if err := privateDirectory(filepath.Dir(dest)); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(root, filepath.FromSlash(relative)), dest, maxSnapshot, 0600); err != nil {
			return err
		}
	}
	if _, err = os.Stat(op.Options.Data); err == nil {
		lock, lockErr := manager.AcquireDataLock(op.Options.Data)
		if lockErr != nil {
			return lockErr
		}
		if err = lock.Close(); err != nil {
			return err
		}
		op.FailedData = op.Options.Data + ".web-failed-" + restoreID
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		op.FailedData = ""
	}
	op.FailedConfig = ""
	if !inside(op.Options.Data, op.Options.Config) {
		if _, err = os.Stat(op.Options.Config); err == nil {
			op.FailedConfig = op.Options.Config + ".web-failed-" + restoreID
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if op.FailedData != "" {
		op.Retained = append(op.Retained, op.FailedData)
	}
	if op.FailedConfig != "" {
		op.Retained = append(op.Retained, op.FailedConfig)
	}
	if err = saveOperation(op); err != nil {
		return err
	}
	if op.FailedData != "" {
		if !inside(filepath.Dir(op.Options.Data), op.FailedData) || inside(op.Options.Data, op.FailedData) {
			return errors.New("invalid failed-data path")
		}
		if err = os.Rename(op.Options.Data, op.FailedData); err != nil {
			return err
		}
	}
	if err = os.Rename(staged, op.Options.Data); err != nil {
		return err
	}
	if op.FailedConfig != "" {
		if _, err = os.Lstat(op.FailedConfig); !errors.Is(err, os.ErrNotExist) {
			return errors.New("failed-config retention target exists")
		}
		if err = os.Rename(op.Options.Config, op.FailedConfig); err != nil {
			return err
		}
	}
	if err = copyFile(filepath.Join(root, "config.yaml"), op.Options.Config, maxSnapshot, 0600); err != nil {
		return err
	}
	return replaceBinary(filepath.Join(root, "previous-binary"), op.Options.Binary, backup.OriginalHash)
}
func replaceBinary(source, destination, hash string) error {
	stage := destination + ".web-incoming"
	if err := copyFile(source, stage, maxBinary, 0700); err != nil {
		return err
	}
	got, err := fileHash(stage)
	if err != nil || got != hash {
		return errors.New("binary checksum changed during replacement")
	}
	if err = noLinks(destination); err != nil {
		return err
	}
	return renameFile(stage, destination)
}
