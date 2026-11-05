package webupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"cliproxyapi-plus/internal/product"
)

func selectAsset(release product.Release) (product.Asset, error) {
	var selected product.Asset
	suffix := "_" + runtime.GOOS + "_" + runtime.GOARCH + "_portable.tar.gz"
	if runtime.GOOS == "windows" {
		suffix = "_windows_" + runtime.GOARCH + "_portable.zip"
	}
	for _, asset := range release.Assets {
		if strings.HasPrefix(asset.Name, "CLIProxyAPIPlus_") && strings.HasSuffix(asset.Name, suffix) {
			if selected.Name != "" {
				return selected, errors.New("multiple matching platform assets")
			}
			selected = asset
		}
	}
	if selected.Name == "" {
		return selected, errors.New("release has no package for this Portable platform")
	}
	return selected, nil
}
func allowedDownload(index, raw string, allowLocalHTTP ...bool) bool {
	policy := product.ReleaseSourcePolicy{AllowLocalHTTP: len(allowLocalHTTP) > 0 && allowLocalHTTP[0]}
	return policy.AllowsAsset(index, raw)
}
func download(ctx context.Context, index string, asset product.Asset, path string, source *http.Client, allowLocalHTTP ...bool) error {
	if !allowedDownload(index, asset.URL, allowLocalHTTP...) {
		return errors.New("asset origin is not permitted by the configured release policy")
	}
	client := *source
	client.Timeout = 10 * time.Minute
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || !allowedDownload(index, req.URL.String(), allowLocalHTTP...) {
			return errors.New("download redirect origin rejected")
		}
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.ContentLength > maxArchive {
		return errors.New("download status or package size rejected")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, err := io.Copy(file, io.LimitReader(response.Body, maxArchive+1))
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		return err
	}
	if n > maxArchive {
		return errors.New("archive exceeds size limit")
	}
	hash, err := fileHash(path)
	if err != nil || !strings.EqualFold(hash, asset.SHA256) {
		return errors.New("downloaded package SHA256 mismatch")
	}
	return nil
}
func extractBinary(archive, name, destination string) error {
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = out.Close()
		if !complete {
			_ = os.Remove(destination)
		}
	}()
	count := 0
	total := int64(0)
	found := false
	seen := map[string]bool{}
	inspect := func(path string, size int64, mode os.FileMode, read io.Reader) error {
		count++
		if count > 50000 || size < 0 || size > 2<<30 {
			return errors.New("archive entry limit exceeded")
		}
		clean := strings.TrimSuffix(path, "/")
		if !safeRelative(clean) || mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return errors.New("archive contains unsafe paths or links")
		}
		key := strings.ToLower(clean)
		if seen[key] {
			return errors.New("archive contains duplicate paths")
		}
		seen[key] = true
		total += size
		if total > 2<<30 {
			return errors.New("uncompressed package exceeds limit")
		}
		if clean != executableName() {
			return nil
		}
		if found || !mode.IsRegular() || size > maxBinary || size < 1 {
			return errors.New("invalid executable entry")
		}
		found = true
		n, e := io.Copy(out, io.LimitReader(read, maxBinary+1))
		if e != nil {
			return e
		}
		if n != size || n > maxBinary {
			return errors.New("executable size mismatch")
		}
		return nil
	}
	if strings.HasSuffix(name, ".zip") {
		archiveFile, e := zip.OpenReader(archive)
		if e != nil {
			return e
		}
		defer archiveFile.Close()
		if len(archiveFile.File) > 50000 {
			return errors.New("archive has too many entries")
		}
		for _, entry := range archiveFile.File {
			if entry.UncompressedSize64 > uint64(2<<30) {
				return errors.New("archive entry too large")
			}
			reader, e := entry.Open()
			if e != nil {
				return e
			}
			e = inspect(entry.Name, int64(entry.UncompressedSize64), entry.Mode(), reader)
			_ = reader.Close()
			if e != nil {
				return e
			}
		}
	} else if strings.HasSuffix(name, ".tar.gz") {
		file, e := os.Open(archive)
		if e != nil {
			return e
		}
		defer file.Close()
		gz, e := gzip.NewReader(file)
		if e != nil {
			return e
		}
		defer gz.Close()
		tr := tar.NewReader(io.LimitReader(gz, (2<<30)+(64<<20)))
		for {
			header, e := tr.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
				return errors.New("tar links and special files are forbidden")
			}
			if e = inspect(header.Name, header.Size, header.FileInfo().Mode(), tr); e != nil {
				return e
			}
		}
	} else {
		return errors.New("unsupported release archive")
	}
	if !found {
		return errors.New("release executable is missing")
	}
	if err = out.Sync(); err != nil {
		return err
	}
	complete = true
	return nil
}

type binaryIdentity struct {
	Version              string `json:"version"`
	Phase                string `json:"phase"`
	OS                   string `json:"os"`
	Arch                 string `json:"arch"`
	UpdateProtocol       int    `json:"web_update_protocol"`
	LocalReleaseHTTP     bool   `json:"local_release_http"`
	SourceDigest         string `json:"product_source_sha256"`
	SourceUpdateProtocol int    `json:"source_update_protocol"`
	CoreCommit           string `json:"core_commit"`
	ManagerCommit        string `json:"manager_commit"`
}

func identify(ctx context.Context, binary string) (binaryIdentity, error) {
	var identity binaryIdentity
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(check, binary, "--version")
	hideProcess(cmd)
	var output bytes.Buffer
	cmd.Stdout = &boundedWriter{Writer: &output, Left: 32768}
	err := cmd.Run()
	if err != nil {
		return identity, fmt.Errorf("candidate version check: %w", err)
	}
	if output.Len() > 32768 {
		return identity, errors.New("version output exceeds limit")
	}
	if err = json.Unmarshal(output.Bytes(), &identity); err != nil {
		return identity, err
	}
	if identity.OS != runtime.GOOS || identity.Arch != runtime.GOARCH || identity.UpdateProtocol != Protocol || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(identity.CoreCommit) || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(identity.ManagerCommit) {
		return identity, errors.New("candidate is not a matching integrated updater-capable Portable program")
	}
	return identity, nil
}
func (c *Controller) prepare(op Operation, asset product.Asset) {
	failure := func(err error) {
		op.State = "failed"
		op.Error = err.Error()
		if file, e := os.OpenFile(filepath.Join(c.opts.operationDir(op.ID), "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); e == nil {
			_, _ = fmt.Fprintln(file, err)
			_ = file.Close()
		}
		_ = saveOperation(&op)
		c.mu.Lock()
		if c.lock != nil {
			_ = c.lock.Close()
			c.lock = nil
		}
		c.active = ""
		c.mu.Unlock()
	}
	dir := c.opts.operationDir(op.ID)
	if op.Kind == "install" {
		op.State = "downloading"
		if err := saveOperation(&op); err != nil {
			failure(err)
			return
		}
		archive := filepath.Join(dir, "package")
		if err := c.materializeArchive(op, asset, archive); err != nil {
			failure(err)
			return
		}
		op.State = "verifying"
		if err := saveOperation(&op); err != nil {
			failure(err)
			return
		}
		candidate := filepath.Join(dir, executableName())
		if err := extractBinary(archive, asset.Name, candidate); err != nil {
			failure(err)
			return
		}
		identity, err := identify(c.ctx, candidate)
		if err != nil || !candidateMatches(op, identity, c.releases.Policy.AllowLocalHTTP) {
			failure(errors.New("candidate identity/version does not match the release"))
			return
		}
		op.TargetHash, err = fileHash(candidate)
		if err != nil {
			failure(err)
			return
		}
	}
	if err := c.ctx.Err(); err != nil {
		failure(err)
		return
	}
	worker := filepath.Join(dir, "worker")
	if runtime.GOOS == "windows" {
		worker += ".exe"
	}
	if err := copyFile(c.opts.Binary, worker, maxBinary, 0700); err != nil {
		failure(err)
		return
	}
	if hash, err := fileHash(worker); err != nil || hash != op.OriginalHash {
		failure(errors.New("current executable changed before helper handoff"))
		return
	}
	op.State = "awaiting_shutdown"
	if err := saveOperation(&op); err != nil {
		failure(err)
		return
	}
	cmd := exec.Command(worker, "--web-update-worker", op.ID, "--config", c.opts.Config, "--data-dir", c.opts.Data)
	log, err := os.OpenFile(filepath.Join(dir, "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		failure(err)
		return
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Dir = c.opts.WorkDir
	detachProcess(cmd)
	if err = cmd.Start(); err != nil {
		failure(err)
		return
	}
	go func() { _ = cmd.Wait() }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			failure(c.ctx.Err())
			return
		case <-deadline.C:
			failure(errors.New("update worker did not acknowledge; current service has not been stopped"))
			return
		case <-ticker.C:
			var ready struct {
				PID int `json:"pid"`
			}
			if readJSON(filepath.Join(dir, "worker-ready.json"), &ready) == nil && ready.PID == cmd.Process.Pid {
				if c.ctx.Err() != nil {
					failure(c.ctx.Err())
					return
				}
				if err = exclusiveMarker(c.opts.Data+".maintenance", op.ID); err != nil {
					failure(err)
					return
				}
				c.stop()
				return
			}
		}
	}
}
func exclusiveMarker(path, id string) error {
	if err := noLinks(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, e := f.WriteString(id)
	return errors.Join(e, f.Sync(), f.Close())
}

type boundedWriter struct {
	io.Writer
	Left int
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if len(p) > b.Left {
		return 0, errors.New("command output exceeds limit")
	}
	n, err := b.Writer.Write(p)
	b.Left -= n
	return n, err
}

func candidateMatches(op Operation, identity binaryIdentity, localHTTP bool) bool {
	return identity.Version == op.TargetVersion && (op.SourceRequest == nil || (identity.SourceUpdateProtocol == 1 && identity.CoreCommit == op.SourceCoreCommit && identity.ManagerCommit == op.SourceManagerCommit)) && (op.TargetSourceDigest == "" || identity.SourceDigest == op.TargetSourceDigest) && (!localHTTP || identity.LocalReleaseHTTP)
}
func (c *Controller) materializeArchive(op Operation, asset product.Asset, archive string) error {
	if op.SourceRequest == nil {
		return download(c.ctx, c.releases.URL, asset, archive, c.releases.Client, c.releases.Policy.AllowLocalHTTP)
	}
	var source SourceCandidate
	if err := readJSON(filepath.Join(c.opts.operationDir(op.ID), "source-result.json"), &source); err != nil || source.SHA256 != asset.SHA256 || !inside(c.opts.operationDir(op.ID), source.Archive) {
		return errors.New("source artifact receipt is missing or invalid")
	}
	if err := copyFile(source.Archive, archive, maxArchive, 0600); err != nil {
		return err
	}
	sum, err := fileHash(archive)
	if err != nil || !strings.EqualFold(sum, asset.SHA256) {
		return errors.New("source archive checksum mismatch")
	}
	return nil
}
