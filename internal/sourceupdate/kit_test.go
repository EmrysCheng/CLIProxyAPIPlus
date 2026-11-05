package sourceupdate

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"cliproxyapi-plus/internal/webupdate"
)

func bytesHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func kitFixture(t *testing.T) (Options, kitManifest) {
	t.Helper()
	root := t.TempDir()
	files := map[string][]byte{"VERSION": []byte("1.0.0\n"), "scripts/worker.py": []byte("trusted code\n"), "upstream/cliproxyapi/a.go": []byte("core fixture"), "upstream/cpa-manager-plus/b.go": []byte("manager fixture")}
	records := map[string]string{}
	productRecords := map[string]string{}
	for name, data := range files {
		records[name] = bytesHash(data)
		if productKey(name) {
			productRecords[name] = records[name]
		}
	}
	keys := []string{}
	for key := range productRecords {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, key := range keys {
		parts = append(parts, strconv.Quote(key)+": "+strconv.Quote(productRecords[key]))
	}
	source := bytesHash([]byte("{" + strings.Join(parts, ", ") + "}"))
	current := map[string]webupdate.SourceTarget{"cliproxyapi": {Version: "v1.0.0", Commit: strings.Repeat("a", 40)}, "cpa-manager-plus": {Version: "v1.0.0", Commit: strings.Repeat("b", 40)}}
	projects := map[string]any{}
	for _, id := range projectOrder {
		hash := sha256.New()
		prefix := "upstream/" + id + "/"
		names := []string{}
		for name := range files {
			if strings.HasPrefix(name, prefix) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			relative := []byte(strings.TrimPrefix(name, prefix))
			var size [8]byte
			binary.BigEndian.PutUint32(size[:4], uint32(len(relative)))
			hash.Write(size[:4])
			hash.Write(relative)
			binary.BigEndian.PutUint64(size[:], uint64(len(files[name])))
			hash.Write(size[:])
			hash.Write(files[name])
		}
		projects[id] = map[string]string{"commit": current[id].Commit, "repository": repositories[id], "tree_sha256": hex.EncodeToString(hash.Sum(nil))}
	}
	lock, _ := json.Marshal(map[string]any{"projects": projects})
	files["upstream.lock.json"] = lock
	records["upstream.lock.json"] = bytesHash(lock)
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	kit := kitManifest{Schema: 1, SourceDigest: source, LockDigest: records["upstream.lock.json"], Records: records, ProductRecords: productRecords}
	raw, _ := json.Marshal(kit)
	if err := os.WriteFile(filepath.Join(root, "source-kit.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return Options{Workspace: root, SourceDigest: source, LockDigest: kit.LockDigest, Current: current}, kit
}
func TestKitCopyRejectsTamperingAndNeverCopiesCaches(t *testing.T) {
	opts, _ := kitFixture(t)
	if err := os.Mkdir(filepath.Join(opts.Workspace, ".build"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.Workspace, ".build", "secret.key"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "job")
	if err := copyKit(opts, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, ".build", "secret.key")); !os.IsNotExist(err) {
		t.Fatal("copied private/cache files")
	}
	if err := os.WriteFile(filepath.Join(opts.Workspace, "scripts", "worker.py"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyKit(opts, filepath.Join(t.TempDir(), "job")); err == nil {
		t.Fatal("modified source was executed/copied")
	}
}
func TestKitRecordsCannotForgeBinaryIdentity(t *testing.T) {
	opts, kit := kitFixture(t)
	kit.ProductRecords["scripts/worker.py"] = strings.Repeat("0", 64)
	kit.Records["scripts/worker.py"] = strings.Repeat("0", 64)
	raw, _ := json.Marshal(kit)
	if err := os.WriteFile(filepath.Join(opts.Workspace, "source-kit.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyKit(opts, filepath.Join(t.TempDir(), "job")); err == nil {
		t.Fatal("manifest hash forgery accepted")
	}
}
func TestPackagedRecipeCrossLanguageVerification(t *testing.T) {
	path := os.Getenv("CLIPROXY_PLUS_SOURCE_KIT_TEST")
	if path == "" {
		t.Skip("opt-in packaged source recipe acceptance")
	}
	kit, err := loadKit(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(path, "upstream.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Projects map[string]struct {
			Ref    string `json:"ref"`
			Commit string `json:"commit"`
		} `json:"projects"`
	}
	if json.Unmarshal(raw, &lock) != nil {
		t.Fatal("invalid lock")
	}
	current := map[string]webupdate.SourceTarget{}
	for id, project := range lock.Projects {
		current[id] = webupdate.SourceTarget{Version: project.Ref, Commit: project.Commit}
	}
	opts := Options{Workspace: path, SourceDigest: kit.SourceDigest, LockDigest: kit.LockDigest, Current: current}
	if err := copyKit(opts, filepath.Join(t.TempDir(), "verified-source")); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"passed": true, "product_source_sha256": kit.SourceDigest, "upstream_lock_sha256": kit.LockDigest, "time": time.Now().UTC().Format(time.RFC3339), "files": len(kit.Records), "checks": []string{"Python manifest vs Go canonical hash", "binary-bound product/lock identity", "every source file checksum", "both immutable upstream tree digests", "private isolated copy only"}}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	destination := os.Getenv("CLIPROXY_PLUS_SOURCE_KIT_REPORT")
	if destination == "" {
		t.Fatal("acceptance report destination required")
	}
	if err := os.WriteFile(destination, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}
