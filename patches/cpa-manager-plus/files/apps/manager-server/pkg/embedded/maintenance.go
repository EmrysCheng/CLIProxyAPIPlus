package embedded

import (
	"context"
	"fmt"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/adminreset"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"io"
	"path/filepath"
)

func ResetAdmin(ctx context.Context, dataDir string, stdout, stderr io.Writer) error {
	return adminreset.Run(ctx, []string{"--db-path", filepath.Join(dataDir, "usage.sqlite"), "--admin-key-file", filepath.Join(dataDir, "admin.key")}, stdout, stderr)
}

// CheckData runs native schema upgrades and integrity checks while offline.
func CheckData(ctx context.Context, opts Options) error {
	path := filepath.Join(opts.DataDir, "usage.sqlite")
	lock, err := processlock.Acquire(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	keyPath := filepath.Join(opts.DataDir, "data.key")
	if err := sqliterepo.RequireExistingDataKeyForEncryptedCPAConnection(ctx, path, "", keyPath); err != nil {
		return err
	}
	key, _, err := security.LoadOrCreateDataKey("", keyPath)
	if err != nil {
		return err
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		return err
	}
	db, err := store.Open(path, protector)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.CheckIntegrity(ctx); err != nil {
		return err
	}
	if err := db.RunDerivedStartupMaintenance(ctx); err != nil {
		return fmt.Errorf("native data maintenance: %w", err)
	}
	return nil
}

// AcquireDataLock holds the same cross-process lock as Manager's normal runtime.
// The caller must release it before starting migration or a replacement service.
func AcquireDataLock(dataDir string) (io.Closer, error) {
	return processlock.Acquire(filepath.Join(dataDir, "usage.sqlite"))
}
