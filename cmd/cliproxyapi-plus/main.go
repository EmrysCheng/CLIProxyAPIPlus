// CLIProxyAPIPlus is currently a integrated single-process proxy and management server.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	// Keep portable installs independent of a Go SDK or host IANA database.
	_ "time/tzdata"

	"cliproxyapi-plus/internal/buildinfo"
	"cliproxyapi-plus/internal/integration"
	"cliproxyapi-plus/internal/policy"
	"cliproxyapi-plus/internal/private"
	"cliproxyapi-plus/internal/product"
	"cliproxyapi-plus/internal/sourceupdate"
	"cliproxyapi-plus/internal/webupdate"
	"github.com/gin-gonic/gin"
	sdkapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	coreembedded "github.com/router-for-me/CLIProxyAPI/v8/sdk/embedded"
	manager "github.com/seakee/cpa-manager-plus/apps/manager-server/pkg/embedded"
	"golang.org/x/crypto/bcrypt"
)

var Version = "development"
var SourceDigest = "development"
var BuildDate = "unknown"
var LockDigest = "development"
var CoreVersion = "v8.0.53"
var CoreCommit = "01b24f1f95dc9b309765212c52b25b25e005ff01"
var ManagerVersion = "v1.14.10"
var ManagerCommit = "a67ed8834a914ab24c36d2490fa6bb6d4166724c"

type versionInfo struct {
	SourceUpdateProtocol int    `json:"source_update_protocol"`
	LockDigest           string `json:"upstream_lock_sha256"`
	OS                   string `json:"os"`
	Arch                 string `json:"arch"`
	WebUpdateProtocol    int    `json:"web_update_protocol"`
	LocalReleaseHTTP     bool   `json:"local_release_http"`
	SourceDigest         string `json:"product_source_sha256"`
	BuildDate            string `json:"build_date"`
	Version              string `json:"version"`
	Phase                string `json:"phase"`
	CoreVersion          string `json:"core_version"`
	CoreCommit           string `json:"core_commit"`
	ManagerVersion       string `json:"manager_version"`
	ManagerCommit        string `json:"manager_commit"`
}

func versions() versionInfo {
	return versionInfo{OS: runtime.GOOS, Arch: runtime.GOARCH, WebUpdateProtocol: webupdate.Protocol, LocalReleaseHTTP: true, SourceUpdateProtocol: 1, LockDigest: LockDigest, SourceDigest: SourceDigest, BuildDate: BuildDate, Version: Version, Phase: buildinfo.Phase(Version), CoreVersion: CoreVersion, CoreCommit: CoreCommit, ManagerVersion: ManagerVersion, ManagerCommit: ManagerCommit}
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(parent context.Context, args []string) (resultErr error) {
	parent, requestStop := context.WithCancel(parent)
	defer requestStop()
	fs := flag.NewFlagSet("cliproxyapi-plus", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "Native CPA configuration path")
	dataDir := fs.String("data-dir", ".data", "Product keys and Manager SQLite directory")
	listen := fs.String("listen", "", "Override IP:port; defaults to the configuration")
	checkData := fs.Bool("check-data", false, "Offline data migration/integrity validation without listeners")
	inspect := fs.Bool("inspect-config", false, "Print non-secret native configuration paths for backup tooling")
	adopt := fs.String("adopt-config", "", "Import native configuration into a new installation; never modify the source")
	relocate := fs.Bool("relocate-config", false, "Relocate restored auth/TLS paths to data-dir while offline")
	reset := fs.Bool("reset-admin", false, "Offline reset of Manager admin credential from data-dir/admin.key")
	initOnly := fs.Bool("init", false, "Create a new local configuration and credential files; never overwrite")
	showVersion := fs.Bool("version", false, "Print product and dual-upstream build identity")
	allowRemote := fs.Bool("allow-remote", false, "Explicitly allow a non-loopback listener for this build")
	releaseIndex := fs.String("release-index", os.Getenv("CLIPROXY_PLUS_RELEASE_INDEX"), "HTTPS product release index (loopback HTTP requires --allow-local-release-http)")
	allowLocalReleaseHTTP := fs.Bool("allow-local-release-http", false, "Explicitly allow same-origin HTTP release index/packages on literal loopback IPs only; never LAN or DNS names")
	releaseCA := fs.String("release-ca-file", "", "Optional trusted CA PEM for a private HTTPS release source")
	webUpdates := fs.Bool("web-updates", false, "Opt in to standalone Portable web upgrade/restart; never combine with an external restart supervisor")
	sourceUpdates := fs.Bool("source-updates", false, "Explicitly allow verified direct-upstream source builds; requires local Python/Go/Node/npm and --web-updates")
	sourceWorkspace := fs.String("source-workspace", "", "Hash-bound source recipe; defaults to updater-source beside the executable")
	sourceTools := fs.String("source-tools", "", "Operator-only tool paths JSON; defaults to update-tools.json beside the executable")
	updateWorker := fs.String("web-update-worker", "", "Private update helper operation id")
	updateActivation := fs.String("web-update-activation", "", "Private replacement activation id")
	updateRecovery := fs.String("recover-web-update", "", "Offline recovery operation id; run from its saved worker executable")
	localModels := fs.Bool("local-model", true, "Prefer the bundled model catalogs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *showVersion {
		return json.NewEncoder(os.Stdout).Encode(versions())
	}
	releaseLoggingDefaults := coreembedded.UseProductLoggingDefaults()
	defer releaseLoggingDefaults()

	var err error
	*configPath, err = filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	*dataDir, err = filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	if *updateWorker != "" || *updateRecovery != "" {
		if *updateWorker != "" && *updateRecovery != "" {
			return errors.New("choose one update worker mode")
		}
		id := *updateWorker
		if *updateRecovery != "" {
			id = *updateRecovery
		}
		return webupdate.RunWorker(parent, *dataDir, *configPath, id, *updateRecovery != "")
	}
	if *checkData {
		return manager.CheckData(parent, manager.Options{DataDir: *dataDir})
	}
	if *inspect {
		return inspectConfig(*configPath, os.Stdout)
	}
	if *reset {
		return resetAdmin(parent, *dataDir)
	}
	if *relocate {
		return relocateConfig(*configPath, *dataDir)
	}
	if *adopt != "" {
		source, e := filepath.Abs(*adopt)
		if e != nil {
			return e
		}
		return adoptConfig(source, *configPath, *dataDir, *listen)
	}
	if *initOnly {
		address := *listen
		if address == "" {
			address = "127.0.0.1:8317"
		}
		return initialize(*configPath, *dataDir, address)
	}
	binaryPath, err := os.Executable()
	if err != nil {
		return err
	}
	if *sourceWorkspace == "" {
		*sourceWorkspace = filepath.Join(filepath.Dir(binaryPath), "updater-source")
	}
	*sourceWorkspace, err = filepath.Abs(*sourceWorkspace)
	if err != nil {
		return err
	}
	if *sourceTools == "" {
		*sourceTools = filepath.Join(filepath.Dir(binaryPath), "update-tools.json")
	}
	*sourceTools, err = filepath.Abs(*sourceTools)
	if err != nil {
		return err
	}

	if _, err := os.Stat(*dataDir + ".maintenance"); err == nil {
		if *updateActivation == "" {
			return errors.New("maintenance marker present; finish recovery before starting the service")
		}
		if err := webupdate.ValidateActivation(webupdate.Options{Binary: binaryPath, Config: *configPath, Data: *dataDir}, *updateActivation); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, env := range []string{"PGSTORE_DSN", "GITSTORE_GIT_URL", "OBJECTSTORE_ENDPOINT", "MANAGEMENT_PASSWORD", "CPA_MANAGER_CONFIG", "CPA_MANAGER_PPROF_ADDR"} {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			return fmt.Errorf("%s is not supported by this embedded bootstrap", env)
		}
	}
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load native config (run --init first for a new installation): %w", err)
	}
	if *listen != "" {
		host, port, errParse := parseAddress(*listen)
		if errParse != nil {
			return errParse
		}
		cfg.Host = host
		cfg.Port = port
	}
	if !*allowRemote {
		ip := net.ParseIP(cfg.Host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("Default listener is loopback; use an explicit --allow-remote only after securing access")
		}
	}
	if cfg.Pprof.Enable || cfg.Discovery.Enabled || cfg.Home.Enabled || (cfg.Plugins.Enabled && !coreembedded.PluginsSupported()) {
		return errors.New("This build does not support pprof, discovery, Home or dynamic plugins; disable these features explicitly")
	}
	coreKey, err := readCredential(filepath.Join(*dataDir, "core-management.key"))
	if err != nil {
		return err
	}
	adminKey, err := readCredential(filepath.Join(*dataDir, "admin.key"))
	if err != nil {
		return err
	}
	secret := cfg.RemoteManagement.SecretKey
	if secret != coreKey && bcrypt.CompareHashAndPassword([]byte(secret), []byte(coreKey)) != nil {
		return errors.New("native management secret does not match the product core-management.key; refusing ambiguous credentials")
	}
	// The init-generated file persists these settings so a watcher reload is consistent.
	if cfg.RemoteManagement.AllowRemote || !cfg.RemoteManagement.DisableControlPanel || !cfg.RemoteManagement.DisableAutoUpdatePanel {
		return errors.New("embedded mode requires remote-management.allow-remote=false, disable-control-panel=true and disable-auto-update-panel=true")
	}
	if !filepath.IsAbs(cfg.AuthDir) {
		cfg.AuthDir = filepath.Join(filepath.Dir(*configPath), cfg.AuthDir)
	}
	validate := policy.Frozen(cfg, coreKey, filepath.Dir(*configPath))
	if *webUpdates {
		validate = policy.WithSnapshotKVStoreBoundary(validate, *dataDir)
	}
	// Check before constructing the SDK or opening any newly introduced store.
	// The installed callback below enforces the same boundary on every reload.
	if err = validate(cfg); err != nil {
		return err
	}
	releasePolicy, err := coreembedded.InstallConfigPolicy(validate)
	if err != nil {
		return err
	}
	defer releasePolicy()
	releases, err := product.NewReleasesWithPolicy(*releaseIndex, Version, product.ReleaseSourcePolicy{AllowLocalHTTP: *allowLocalReleaseHTTP})
	if err != nil {
		return err
	}
	if *releaseCA != "" {
		*releaseCA, err = filepath.Abs(*releaseCA)
		if err != nil {
			return err
		}
		if err = releases.TrustCAFile(*releaseCA); err != nil {
			return fmt.Errorf("release certificate authority: %w", err)
		}
	}
	coreembedded.Initialize(*localModels)
	logPath, closeLogging, err := coreembedded.ConfigureLogging(cfg)
	if err != nil {
		return fmt.Errorf("configure application logging: %w", err)
	}
	defer closeLogging()
	if logPath != "" {
		fmt.Fprintf(os.Stderr, "Application log: %s\n", logPath)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	healthAddress, healthErr := webUpdateHealthAddress(cfg.Host, cfg.Port)
	scheme := "http"
	updateOpts := webupdate.Options{Enabled: *webUpdates, Binary: binaryPath, Config: *configPath, Data: *dataDir, Auth: cfg.AuthDir, HealthURL: scheme + "://" + healthAddress, Version: Version, WorkDir: cwd, DisabledReason: webUpdateRuntimeReason()}
	if healthErr != nil {
		updateOpts.DisabledReason = "Web updates require an explicit listener IP and a fixed port; use offline maintenance for hostname/dynamic-port listeners."
	}
	if cfg.TLS.Enable {
		updateOpts.TLSCert, err = filepath.Abs(cfg.TLS.Cert)
		if err != nil {
			return err
		}
		updateOpts.TLSKey, err = filepath.Abs(cfg.TLS.Key)
		if err != nil {
			return err
		}
		updateOpts.HealthURL = "https://" + healthAddress
	}
	updateOpts.Args = []string{"--config", *configPath, "--data-dir", *dataDir, "--listen", net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port)), "--local-model=" + fmt.Sprint(*localModels), "--release-index", *releaseIndex}
	if *allowLocalReleaseHTTP {
		updateOpts.Args = append(updateOpts.Args, "--allow-local-release-http")
	}
	if *releaseCA != "" {
		updateOpts.Args = append(updateOpts.Args, "--release-ca-file", *releaseCA)
	}
	if *webUpdates {
		updateOpts.Args = append(updateOpts.Args, "--web-updates")
	}
	if *allowRemote {
		updateOpts.Args = append(updateOpts.Args, "--allow-remote")
	}
	if *sourceUpdates {
		updateOpts.Args = append(updateOpts.Args, "--source-updates", "--source-workspace", *sourceWorkspace, "--source-tools", *sourceTools)
	}
	var cleanShutdown bool
	updates := webupdate.New(parent, updateOpts, releases, requestStop)
	defer func() { updates.Close(cleanShutdown) }()
	go releases.Run(parent, 6*time.Hour)
	sources := sourceupdate.New(sourceupdate.Options{Enabled: *sourceUpdates && *webUpdates, Workspace: *sourceWorkspace, ToolsFile: *sourceTools, SourceDigest: SourceDigest, LockDigest: LockDigest, Version: Version, Current: map[string]webupdate.SourceTarget{"cliproxyapi": {Version: CoreVersion, Commit: CoreCommit}, "cpa-manager-plus": {Version: ManagerVersion, Commit: ManagerCommit}}}, updates)
	go sources.Run(parent)
	bridge := &integration.Bridge{Version: versions(), Product: product.Handler(releases, updates, sources)}
	coreReady := make(chan struct{})
	service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(*configPath).
		WithServerOptions(sdkapi.WithEngineConfigurator(func(engine *gin.Engine) {
			engine.Use(bridge.Middleware())
			bridge.SetCore(engine)
		})).WithHooks(cliproxy.Hooks{OnAfterStart: func(*cliproxy.Service) { bridge.MarkCoreReady(true); close(coreReady) }}).Build()
	if err != nil {
		return fmt.Errorf("construct core service: %w", err)
	}

	coreCtx, stopCore := context.WithCancel(context.WithoutCancel(parent))
	defer stopCore()
	managerCtx, stopManager := context.WithCancel(context.WithoutCancel(parent))
	defer stopManager()
	coreDone := make(chan error, 1)
	managerDone := make(chan error, 1)
	go func() { coreDone <- service.Run(coreCtx) }()
	go func() {
		select {
		case <-coreReady:
		case <-managerCtx.Done():
			managerDone <- nil
			return
		}
		managerDone <- manager.Run(managerCtx, manager.Options{DataDir: *dataDir, AdminKey: adminKey, ManagementKey: coreKey, Transport: &integration.Transport{Bridge: bridge}, AuthorizerReady: bridge.SetAuthorizer}, func(handler http.Handler) {
			bridge.SetManager(handler)
			if err := webupdate.MarkReady(updateOpts, *updateActivation); err != nil {
				fmt.Fprintln(os.Stderr, "update activation readiness:", err)
				requestStop()
			}
		})
	}()
	fmt.Fprintf(os.Stderr, "CLIProxyAPIPlus %s; management credential file: %s\n", Version, filepath.Join(*dataDir, "admin.key"))
	var result error
	coreExited, managerExited := false, false
	select {
	case <-parent.Done():
	case result = <-coreDone:
		coreExited = true
	case result = <-managerDone:
		managerExited = true
	}
	bridge.MarkCoreReady(false)
	drain, cancelDrain := context.WithTimeout(context.Background(), 30*time.Second)
	drainErr := bridge.Drain(drain)
	cancelDrain()
	stopManager()
	managerDeadline := time.NewTimer(35 * time.Second)
	defer managerDeadline.Stop()
	var managerErr error
	if !managerExited {
		select {
		case managerErr = <-managerDone:
			result = errors.Join(result, managerErr)
		case <-managerDeadline.C:
			managerErr = errors.New("Manager drain deadline exceeded")
			result = errors.Join(result, managerErr)
		}
	}
	bridge.SetManager(nil)
	stopCore()
	coreDeadline := time.NewTimer(35 * time.Second)
	defer coreDeadline.Stop()
	var coreErr error
	if !coreExited {
		select {
		case coreErr = <-coreDone:
			if !errors.Is(coreErr, context.Canceled) {
				result = errors.Join(result, coreErr)
			}
		case <-coreDeadline.C:
			coreErr = errors.New("core drain deadline exceeded")
			result = errors.Join(result, coreErr)
		}
	}
	if drainErr != nil {
		result = errors.Join(result, fmt.Errorf("public requests did not drain before cancellation: %w", drainErr))
	}
	cleanShutdown = (managerErr == nil) && (coreErr == nil || errors.Is(coreErr, context.Canceled) || errors.Is(coreErr, http.ErrServerClosed))
	return result

}

func readCredential(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read credential %s: %w", path, err)
	}
	key := strings.TrimSpace(string(data))
	if len(key) < 32 {
		return "", fmt.Errorf("credential %s is missing or too short", path)
	}
	return key, nil
}

func parseAddress(address string) (string, int, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	if net.ParseIP(host) == nil {
		return "", 0, errors.New("listen host must be an explicit IP address")
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil || port < 1 || port > 65535 || fmt.Sprint(port) != portText {
		return "", 0, errors.New("listen port must be an integer from 1 to 65535")
	}
	return host, port, nil
}

func initialize(configPath, dataDir, address string) error {
	host, port, err := parseAddress(address)
	if err != nil {
		return err
	}
	files := []string{configPath, filepath.Join(dataDir, "core-management.key"), filepath.Join(dataDir, "admin.key"), filepath.Join(dataDir, "api.key")}
	if _, err := os.Stat(dataDir); err == nil {
		return errors.New("init requires a new data directory; refusing to mix credentials with existing data")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, path := range files {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("refusing to overwrite %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	if err := private.Protect(dataDir, true); err != nil {
		return err
	}
	keys := make([]string, 3)
	for i, path := range files[1:] {
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err != nil {
			return err
		}
		keys[i] = hex.EncodeToString(bytes)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := fmt.Fprintln(f, keys[i])
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	content := fmt.Sprintf("host: %q\nport: %d\nauth-dir: %q\napi-keys:\n  - %q\nremote-management:\n  allow-remote: false\n  secret-key: %q\n  disable-control-panel: true\n  disable-auto-update-panel: true\nlogging-to-file: true\nlogs-max-total-size-mb: 100\nusage-statistics-enabled: true\nredis-usage-queue-retention-seconds: 600\n", host, port, filepath.ToSlash(filepath.Join(dataDir, "auths")), keys[2], keys[0])
	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := private.Protect(configPath, false); err != nil {
		_ = f.Close()
		return err
	}
	_, writeErr := f.WriteString(content)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Fprintf(os.Stdout, "Initialized integrated configuration: %s\nCredential files: %s\nPrivate account permissions have been applied to the data directory.\n", configPath, dataDir)
	return nil
}

// Health probes target this listener, not an assumed localhost binding. Wildcard
// listeners use their matching loopback family; explicit LAN IPs remain usable.
func webUpdateHealthAddress(host string, port int) (string, error) {
	if _, _, err := parseAddress(net.JoinHostPort(host, fmt.Sprint(port))); err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	if ip.IsUnspecified() {
		if ip.To4() == nil {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	}
	return net.JoinHostPort(host, fmt.Sprint(port)), nil
}
