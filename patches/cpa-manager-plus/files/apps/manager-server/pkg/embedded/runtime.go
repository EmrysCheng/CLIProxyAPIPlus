package embedded

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/cpatransport"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/lifecycle"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	bootstrapservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrap"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/worker"
)

const UpstreamURL = "http://cliproxy.internal"

type Options struct {
	DataDir         string
	AdminKey        string
	ManagementKey   string
	Transport       http.RoundTripper
	AuthorizerReady func(func(context.Context, string) (bool, error))
}

// Config preserves the native Manager configuration for the standalone entrypoint.
type Config = config.Config

// Run embeds Manager without opening a listener.
func Run(parent context.Context, opts Options, ready func(http.Handler)) error {
	if opts.DataDir == "" || opts.AdminKey == "" || opts.ManagementKey == "" || opts.Transport == nil || ready == nil {
		return errors.New("embedded Manager requires directory, credentials, transport and readiness callback")
	}
	cfg := config.Config{
		DataDir: opts.DataDir, DBPath: filepath.Join(opts.DataDir, "usage.sqlite"),
		CPAUpstreamURL: UpstreamURL, ManagementKey: opts.ManagementKey, AdminKey: opts.AdminKey,
		DataKeyPath: filepath.Join(opts.DataDir, "data.key"), CollectorMode: "http",
		Queue: "usage", PopSide: "right", BatchSize: 100, PollInterval: 500 * time.Millisecond,
		QueryLimit: 50000, DashboardHourlyRollupEnabled: true,
		UsageImportChunkBytes:     config.DefaultUsageImportChunkBytes,
		UsageImportDiskQuotaBytes: config.DefaultUsageImportDiskQuotaBytes,
		UsageImportMaxSessions:    config.DefaultUsageImportMaxSessions,
		UsageImportSessionTTL:     config.DefaultUsageImportSessionTTL,
		UsageArchiveDir:           filepath.Join(opts.DataDir, "usage-archives"),
		UsageArchiveRetentionDays: config.DefaultUsageArchiveRetentionDays,
	}
	return runConfigured(parent, cfg, opts.Transport, ready, opts.AuthorizerReady, false)
}

func runConfigured(parent context.Context, cfg Config, transport http.RoundTripper, ready func(http.Handler), authReady func(func(context.Context, string) (bool, error)), standalone bool) (resultErr error) {
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return err
	}
	var release func()
	if transport != nil {
		var err error
		release, err = cpatransport.Install(transport)
		if err != nil {
			return err
		}
	}
	ctx, group := lifecycle.New(parent)
	stop := group.Cancel
	_ = stop
	var walMaintenance *sqliterepo.WALMaintenance
	var db *store.Store
	var closeLock func() error
	var drainOnce sync.Once
	drain := func() {
		drainOnce.Do(func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := group.Wait(shutdown); err != nil {
				resultErr = errors.Join(resultErr, err)
				if !group.Drained() {
					return
				}
			}
			if release != nil {
				release()
			}
			if walMaintenance != nil {
				resultErr = errors.Join(resultErr, walMaintenance.Close())
			}
			if db != nil {
				resultErr = errors.Join(resultErr, db.Close())
			}
			if closeLock != nil {
				resultErr = errors.Join(resultErr, closeLock())
			}
		})
	}
	defer drain()

	databaseLock, err := processlock.Acquire(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("acquire manager database process lock: %v", err)
	}
	closeLock = databaseLock.Close
	cfg.DBPath = databaseLock.DatabasePath()
	if err := sqliterepo.RequireExistingDataKeyForEncryptedCPAConnection(
		context.Background(),
		cfg.DBPath,
		cfg.DataKey,
		cfg.DataKeyPath,
	); err != nil {
		return fmt.Errorf("validate data key availability: %v", err)
	}
	dataKey, dataKeyCreated, err := security.LoadOrCreateDataKey(cfg.DataKey, cfg.DataKeyPath)
	if err != nil {
		return fmt.Errorf("load data key: %v", err)
	}
	protector, err := security.NewProtector(dataKey)
	if err != nil {
		return fmt.Errorf("initialize secret protector: %v", err)
	}
	db, err = store.Open(cfg.DBPath, protector)
	if err != nil {
		return fmt.Errorf("open sqlite: %v", err)
	}

	bootstrapResult, err := bootstrapservice.Run(ctx, cfg, db, dataKeyCreated)
	if err != nil {
		return fmt.Errorf("bootstrap manager server: %v", err)
	}
	if bootstrapResult.GeneratedAdminKey != "" {
		if !standalone {
			return errors.New("unexpected generated admin key in embedded mode")
		}
		keyPath := filepath.Join(cfg.DataDir, "admin.bootstrap.key")
		if err := os.WriteFile(keyPath, []byte(bootstrapResult.GeneratedAdminKey+"\n"), 0600); err != nil {
			return err
		}
		log.Printf("initial admin credential saved to %s", keyPath)
	} else {
		log.Printf("CPA Manager Plus admin credential initialized")
	}
	if bootstrapResult.DataKeyCreated {
		log.Printf("CPA Manager Plus data key created at %s", cfg.DataKeyPath)
	}
	if bootstrapResult.MigratedLegacy {
		log.Printf("CPA Manager Plus legacy data migrated")
	}

	manager := collector.NewManager(cfg, db)
	collectorService := collectorservice.New(manager)
	collectorWorker := worker.NewCollectorWorker(cfg, db, collectorService)
	walMaintenance, err = sqliterepo.NewWALMaintenance(cfg.DBPath)
	if err != nil {
		log.Printf("configure SQLite WAL maintenance: %v", err)
	} else {
		walMaintenance.Start(ctx)
	}

	serverApp := httpapi.New(cfg, db, manager)
	if authReady != nil {
		authReady(serverApp.AppContext().AdminAuthService.VerifyHeader)
	}
	serverApp.AppContext().DatabaseMaintenance = walMaintenance
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, 10*time.Second)
	defer cancelRecovery()
	if err := serverApp.AppContext().CodexInspectionService.Recover(recoveryCtx); err != nil {
		return fmt.Errorf("recover codex inspection runs: %w", err)
	}
	cancelRecovery()
	automationSettingsService := serverApp.AppContext().AccountProcessingPolicyService
	runtimeSettings := automationSettingsService.RuntimeSettings(ctx)
	rateLimitAutoDisableWorker := worker.NewRateLimitAutoDisableWorkerWithMutationCoordinator(
		db,
		serverApp.AppContext().AuthFileMutationCoordinator,
		collector.RuntimeConfig{
			CPAUpstreamURL: cfg.CPAUpstreamURL,
			ManagementKey:  cfg.ManagementKey,
		},
	)
	serverApp.AppContext().QuotaCooldownRecoveryService = rateLimitAutoDisableWorker
	accountActionWorker := worker.NewAccountActionCandidateWorkerWithMutationCoordinator(
		db,
		serverApp.AppContext().AuthFileMutationCoordinator,
		runtimeSettings.AccountActionsAutoDisable,
	)
	accountHistoryRollupWorker := worker.NewAccountHistoryRollupWorker(db)
	usageDerivedRollupWorker := worker.NewUsagePricingRollupWorker(db)
	serverApp.AppContext().ModelPriceService.SetPricesChangedNotifier(usageDerivedRollupWorker.Wake)
	var usageHourlyAggregateWorker *worker.UsageHourlyAggregateWorker
	if cfg.DashboardHourlyRollupEnabled {
		usageHourlyAggregateWorker = worker.NewUsageHourlyAggregateWorker(db)
	}
	var usageArchiveRetentionWorker *worker.UsageArchiveRetentionWorker
	if cfg.UsageArchiveRetentionEnabled && cfg.UsageArchiveRetentionDays > 0 && cfg.DashboardHourlyRollupEnabled {
		usageArchiveRetentionWorker = worker.NewUsageArchiveRetentionWorker(
			serverApp.AppContext().UsageService,
			cfg.UsageArchiveRetentionDays,
		)
	}
	serverApp.AppContext().UsageService.SetEventsInsertedNotifier(func() {
		accountHistoryRollupWorker.Wake()
		usageDerivedRollupWorker.Wake()
		if usageHourlyAggregateWorker != nil {
			usageHourlyAggregateWorker.Wake()
		}
	})
	automationRuntime := worker.NewAutomationRuntime(
		automationSettingsService,
		manager,
		rateLimitAutoDisableWorker,
		accountActionWorker,
	)
	serverApp.AppContext().AutomationRuntimeService = automationRuntime
	manager.SetUsageEventHandler(worker.NewUsageEventFanout(
		automationRuntime.UsageEventHandler(),
		accountHistoryRollupWorker,
		usageDerivedRollupWorker,
		usageHourlyAggregateWorker,
	))

	codexInspectionWorker := worker.NewCodexInspectionWorker(serverApp.AppContext().Store, serverApp.AppContext().CodexInspectionService)
	if err := serverApp.AppContext().UsageService.StartImportSessionCleanup(ctx); err != nil {
		return fmt.Errorf("start usage import cleanup: %w", err)
	}
	if err := serverApp.AppContext().UsageService.StartArchiveJobs(ctx); err != nil {
		return fmt.Errorf("start usage archive jobs: %w", err)
	}

	// The core or standalone listener is bound before post-listen maintenance.
	ready(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCtx, cancel := context.WithCancel(group.Attach(r.Context()))
		defer cancel()
		stopCancel := context.AfterFunc(ctx, cancel)
		defer stopCancel()
		done := make(chan struct{})
		if !lifecycle.Go(requestCtx, func() { defer close(done); serverApp.Handler().ServeHTTP(w, r.WithContext(requestCtx)) }) {
			http.Error(w, "Manager is stopping", 503)
			return
		}
		<-done
	}))

	if err := db.RunDerivedStartupMaintenance(ctx); err != nil && ctx.Err() == nil {
		return fmt.Errorf("required derived startup maintenance: %w", err)
	}
	if ctx.Err() == nil {
		log.Printf("[startup] starting background workers")
		automationRuntime.Start(ctx)
		codexInspectionWorker.Start(ctx)
		accountHistoryRollupWorker.Start(ctx)
		usageDerivedRollupWorker.Start(ctx)
		if usageHourlyAggregateWorker != nil {
			usageHourlyAggregateWorker.Start(ctx)
		}
		db.StartDerivedMaintenance(ctx)
		collectorWorker.Start(ctx)
		worker.NewLegacyQuotaSnapshotMigrationWorker(db).Start(ctx)
	}

	usageCacheAccountingMigrationWorker := worker.NewUsageCacheAccountingMigrationWorker(db, func() {
		accountHistoryRollupWorker.Wake()
		usageDerivedRollupWorker.Wake()
		if usageHourlyAggregateWorker != nil {
			usageHourlyAggregateWorker.Wake()
		}
		lifecycle.Go(ctx, func() {
			if err := waitForUsageResponseMetadataBackfill(ctx, db); err != nil {
				if ctx.Err() == nil {
					log.Printf("usage response metadata backfill: %v", err)
				}
				return
			}
			if usageArchiveRetentionWorker != nil {
				usageArchiveRetentionWorker.Start(ctx)
			}
		})
	})
	if ctx.Err() == nil {
		usageCacheAccountingMigrationWorker.Start(ctx)
	}

	if standalone {
		lifecycle.Go(ctx, func() { serverApp.AppContext().UpdateCheckService.Run(ctx) })
	}

	select {
	case <-ctx.Done():
	case err := <-group.Errors():
		resultErr = err
	}
	// Cancel and wait for every registered task before closing the shared database.
	group.Cancel()
	resultErr = errors.Join(resultErr, stopCodexInspectionWorker(codexInspectionWorker, 20*time.Second))
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	collectorWorker.Stop(shutdownCtx)
	if usageArchiveRetentionWorker != nil {
		if err := usageArchiveRetentionWorker.StopAndWait(shutdownCtx); err != nil {
			return err
		}
	}
	if err := serverApp.AppContext().UsageService.WaitArchiveJobs(shutdownCtx); err != nil {
		return err
	}
	return resultErr
}

type codexInspectionStopper interface {
	StopAndWait(context.Context) error
}

func stopCodexInspectionWorker(stopper codexInspectionStopper, timeout time.Duration) error {
	if stopper == nil {
		return nil
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), timeout)
	err := stopper.StopAndWait(shutdownCtx)
	cancelShutdown()
	if err == nil {
		return nil
	}
	// Shutdown must remain bounded. The worker has already fenced new starts and
	// cancelled owned work; if it still cannot finish within the grace period,
	// leave an explicit error for the supervisor and let startup recovery reclaim
	// the expired lease instead of hanging the process indefinitely.
	log.Printf("shutdown codex inspection worker: %v; continuing bounded process shutdown", err)
	return err
}

const usageResponseMetadataBackfillRetryDelay = 5 * time.Second

func waitForUsageResponseMetadataBackfill(ctx context.Context, db *store.Store) error {
	return waitForBackfillReadiness(ctx, usageResponseMetadataBackfillRetryDelay, func(runCtx context.Context) error {
		return runUsageResponseMetadataBackfill(runCtx, db)
	})
}

func waitForBackfillReadiness(ctx context.Context, retryDelay time.Duration, run func(context.Context) error) error {
	if run == nil {
		return errors.New("usage response metadata backfill is not configured")
	}
	if retryDelay <= 0 {
		retryDelay = time.Second
	}
	for {
		err := run(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("usage response metadata backfill failed; will retry: %v", err)
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func runUsageResponseMetadataBackfill(ctx context.Context, db *store.Store) error {
	const batchLimit = 1000
	total := 0
	for {
		updated, err := db.BackfillUsageResponseMetadata(ctx, batchLimit)
		if err != nil {
			return err
		}
		if updated == 0 {
			if total > 0 {
				log.Printf("usage response metadata backfill completed: updated=%d", total)
			}
			return nil
		}
		total += updated
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
