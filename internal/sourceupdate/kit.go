package sourceupdate

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type kitManifest struct {
	Schema         int               `json:"schema_version"`
	SourceDigest   string            `json:"product_source_sha256"`
	LockDigest     string            `json:"upstream_lock_sha256"`
	ProductRecords map[string]string `json:"product_records"`
	Records        map[string]string `json:"records"`
}

func loadKit(path string) (kitManifest, error) {
	var kit kitManifest
	file, err := os.Open(filepath.Join(path, "source-kit.json"))
	if err != nil {
		return kit, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return kit, errors.New("source manifest exceeds limit")
	}
	if json.Unmarshal(raw, &kit) != nil || kit.Schema != 1 || len(kit.Records) == 0 || len(kit.Records) > 20000 {
		return kit, errors.New("invalid source manifest")
	}
	return kit, nil
}
func checkKitHeader(opts Options) error {
	kit, err := loadKit(opts.Workspace)
	if err != nil {
		return err
	}
	if !sourceHashPattern.MatchString(opts.SourceDigest) || !sourceHashPattern.MatchString(opts.LockDigest) || kit.SourceDigest != opts.SourceDigest || kit.LockDigest != opts.LockDigest {
		return errors.New("source recipe does not match installed program")
	}
	return nil
}
func safeKitName(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:\x00\r\n") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") || part == ".build" || strings.TrimRight(part, " .") != part {
			return false
		}
	}
	return true
}
func productKey(name string) bool {
	first := strings.Split(name, "/")[0]
	switch first {
	case "cmd", "internal", "patches", "scripts", "tests", "go.mod", "VERSION", ".github", "Dockerfile", "compose.yaml", ".dockerignore", ".gitattributes", ".gitignore", "AGENTS.md", "README.md", "README_CN.md", "THIRD_PARTY_NOTICES.md", "docs":
		return true
	}
	return false
}

// copyKit executes no code. It verifies the installed source and lock identities,
// copies only hash-listed files to the private job, then verifies both snapshot
// tree digests. Old .build caches or extra Python modules are never copied.
func copyKit(opts Options, destination string) error {
	kit, err := loadKit(opts.Workspace)
	if err != nil {
		return err
	}
	if err := checkKitHeader(opts); err != nil {
		return err
	}
	keys := make([]string, 0, len(kit.ProductRecords))
	for key, value := range kit.ProductRecords {
		if !safeKitName(key) || !productKey(key) || !sourceHashPattern.MatchString(value) || kit.Records[key] != value {
			return errors.New("invalid product recipe records")
		}
		for _, c := range key {
			if c > 127 || c < 32 {
				return errors.New("product recipe names must be portable ASCII")
			}
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strconv.Quote(key)+": "+strconv.Quote(kit.ProductRecords[key]))
	}
	sum := sha256.Sum256([]byte("{" + strings.Join(parts, ", ") + "}"))
	if hex.EncodeToString(sum[:]) != opts.SourceDigest {
		return errors.New("product recipe records do not match binary source identity")
	}
	if kit.Records["upstream.lock.json"] != opts.LockDigest {
		return errors.New("source lock identity mismatch")
	}
	root, err := os.OpenRoot(opts.Workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	names := make([]string, 0, len(kit.Records))
	for name := range kit.Records {
		names = append(names, name)
	}
	sort.Strings(names)
	total := int64(0)
	for _, name := range names {
		if !safeKitName(name) || (!productKey(name) && name != "upstream.lock.json" && !strings.HasPrefix(name, "upstream/cliproxyapi/") && !strings.HasPrefix(name, "upstream/cpa-manager-plus/")) {
			return errors.New("unlisted source recipe scope")
		}
		if productKey(name) && kit.ProductRecords[name] != kit.Records[name] {
			return errors.New("unbound product file in source recipe")
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("source recipe file is not a regular file: %s", name)
		}
		total += info.Size()
		if total > 512<<20 || info.Size() > 128<<20 {
			return errors.New("source recipe exceeds size limits")
		}
		input, err := root.Open(name)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			input.Close()
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			input.Close()
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, info.Size()+1))
		input.Close()
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil || n != info.Size() || hex.EncodeToString(hash.Sum(nil)) != kit.Records[name] {
			return fmt.Errorf("source recipe file changed: %s", name)
		}
	}
	raw, err := os.ReadFile(filepath.Join(destination, "upstream.lock.json"))
	if err != nil {
		return err
	}
	var lock struct {
		Projects map[string]struct {
			Commit     string `json:"commit"`
			Repository string `json:"repository"`
			Tree       string `json:"tree_sha256"`
		} `json:"projects"`
	}
	if json.Unmarshal(raw, &lock) != nil || len(lock.Projects) != 2 {
		return errors.New("invalid source recipe upstream lock")
	}
	for _, id := range projectOrder {
		item := lock.Projects[id]
		if item.Commit != opts.Current[id].Commit || item.Repository != repositories[id] {
			return errors.New("source recipe pins do not match running upstreams")
		}
		digest := sha256.New()
		prefix := "upstream/" + id + "/"
		for _, name := range names {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			relative := []byte(strings.TrimPrefix(name, prefix))
			var size [8]byte
			binary.BigEndian.PutUint32(size[:4], uint32(len(relative)))
			digest.Write(size[:4])
			digest.Write(relative)
			file, err := os.Open(filepath.Join(destination, filepath.FromSlash(name)))
			if err != nil {
				return err
			}
			info, err := file.Stat()
			if err != nil {
				file.Close()
				return err
			}
			binary.BigEndian.PutUint64(size[:], uint64(info.Size()))
			digest.Write(size[:])
			_, err = io.Copy(digest, file)
			file.Close()
			if err != nil {
				return err
			}
		}
		if hex.EncodeToString(digest.Sum(nil)) != item.Tree {
			return errors.New("immutable upstream snapshot hash mismatch")
		}
	}
	encoded, _ := json.Marshal(kit)
	return os.WriteFile(filepath.Join(destination, "source-kit.json"), encoded, 0600)
}
