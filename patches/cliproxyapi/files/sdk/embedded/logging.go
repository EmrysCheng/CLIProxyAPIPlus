package embedded

import (
	"fmt"
	stdlog "log"
	"os"
	"path/filepath"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// standardLogWriter forwards Manager's standard-library logs to the shared logger.
// Resolve the destination on every write so native logging config reloads apply to
// both components, rather than leaving Manager attached to a closed rotating file.
type standardLogWriter struct{}

func (standardLogWriter) Write(p []byte) (int, error) {
	log.Info(strings.TrimRight(string(p), "\r\n"))
	return len(p), nil
}

// ConfigureLogging installs the native rotating logger before either service starts.
// The returned cleanup must run after both services have drained. Native management
// APIs remain responsible for hot-reloading logging-to-file and disk limits.
func ConfigureLogging(cfg *config.Config) (string, func(), error) {
	if cfg == nil {
		return "", nil, fmt.Errorf("missing logging configuration")
	}
	if err := logging.ConfigureLogOutput(cfg); err != nil {
		return "", nil, err
	}
	resetOutput := func() {
		_ = logging.ConfigureLogOutput(&config.Config{})
	}
	var path string
	if cfg.LoggingToFile {
		var err error
		path, err = filepath.Abs(filepath.Join(logging.ResolveLogDirectory(cfg), "main.log"))
		if err != nil {
			resetOutput()
			return "", nil, err
		}
		// Lumberjack opens lazily. Check the actual file now so an unwritable log
		// fails startup visibly, instead of losing the application's first logs.
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			err = file.Close()
		}
		if err != nil {
			resetOutput()
			return "", nil, fmt.Errorf("open application log %s: %w", path, err)
		}
	}
	originalWriter, originalFlags, originalPrefix := stdlog.Writer(), stdlog.Flags(), stdlog.Prefix()
	stdlog.SetFlags(0)
	stdlog.SetPrefix("")
	stdlog.SetOutput(standardLogWriter{})
	return path, func() {
		stdlog.SetOutput(originalWriter)
		stdlog.SetFlags(originalFlags)
		stdlog.SetPrefix(originalPrefix)
		resetOutput()
	}, nil
}

// UseProductLoggingDefaults must surround product configuration loads and live
// reloads, and be released after both embedded services have stopped.
func UseProductLoggingDefaults() func() { return internalconfig.EnableEmbeddedLoggingDefaults() }
