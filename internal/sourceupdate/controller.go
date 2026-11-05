// Package sourceupdate checks only the two fixed upstreams and builds isolated,
// commit-pinned candidates. It never replaces a standalone upstream executable.
package sourceupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"cliproxyapi-plus/internal/product"
	"cliproxyapi-plus/internal/webupdate"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var repositories = map[string]string{"cliproxyapi": "router-for-me/CLIProxyAPI", "cpa-manager-plus": "seakee/CPA-Manager-Plus"}
var projectNames = map[string]string{"cliproxyapi": "CLIProxyAPI", "cpa-manager-plus": "CPA-Manager-Plus"}
var projectOrder = []string{"cliproxyapi", "cpa-manager-plus"}

type Options struct {
	Enabled      bool
	Workspace    string
	ToolsFile    string
	SourceDigest string
	LockDigest   string
	Version      string
	Current      map[string]webupdate.SourceTarget
}
type Project struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Repository     string `json:"repository"`
	CurrentVersion string `json:"current_version"`
	CurrentCommit  string `json:"current_commit"`
	LatestVersion  string `json:"latest_version,omitempty"`
	LatestCommit   string `json:"latest_commit,omitempty"`
	State          string `json:"state"`
	Stale          bool   `json:"stale"`
	Error          string `json:"error,omitempty"`
	LastChecked    string `json:"last_checked,omitempty"`
}
type Status struct {
	Enabled  bool       `json:"enabled"`
	Reason   string     `json:"reason"`
	Mode     string     `json:"mode"`
	Projects []Project  `json:"projects"`
	Tools    ToolStatus `json:"tools"`
}
type Controller struct {
	opts        Options
	maintenance *webupdate.Controller
	client      *http.Client
	mu          sync.Mutex
	checking    sync.Mutex
	status      Status
	// Test injection is internal, never populated from requests or environment.
	builder      webupdate.SourceBuilder
	toolResolver func(context.Context, string) (Tools, ToolStatus)
}

func New(options Options, maintenance *webupdate.Controller) *Controller {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	c := &Controller{opts: options, maintenance: maintenance, client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 2 || req.URL.Scheme != "https" || req.URL.Host != "api.github.com" || req.URL.User != nil {
			return errors.New("upstream metadata redirect rejected")
		}
		return nil
	}}}
	c.status.Mode = "direct-upstream-source"
	for _, id := range projectOrder {
		current := options.Current[id]
		c.status.Projects = append(c.status.Projects, Project{ID: id, Name: projectNames[id], Repository: repositories[id], CurrentVersion: current.Version, CurrentCommit: current.Commit, State: "not_checked", Stale: true})
	}
	c.status.Reason = "Checking local source-update prerequisites."
	return c
}
func (c *Controller) Snapshot() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := c.status
	status.Projects = append([]Project{}, c.status.Projects...)
	return status
}
func (c *Controller) metadata(ctx context.Context, repo, path string, result any) error {
	request, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+repo+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "CLIProxyAPIPlus-direct-upstream-updater")
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return errors.New("GitHub metadata is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("GitHub rejected metadata request; check connectivity or rate limit")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("upstream metadata exceeds limit")
	}
	if json.Unmarshal(raw, result) != nil {
		return errors.New("upstream metadata is not valid JSON")
	}
	return nil
}
func (c *Controller) Check(ctx context.Context) Status {
	if !c.checking.TryLock() {
		status := c.Snapshot()
		for i := range status.Projects {
			status.Projects[i].Stale = true
		}
		return status
	}
	defer c.checking.Unlock()
	c.mu.Lock()
	for i := range c.status.Projects {
		c.status.Projects[i].Stale = true
	}
	c.mu.Unlock()
	checked := make(chan Project, 2)
	for _, id := range projectOrder {
		go func(id string) {
			current := c.opts.Current[id]
			p := Project{ID: id, Name: projectNames[id], Repository: repositories[id], CurrentVersion: current.Version, CurrentCommit: current.Commit, State: "check_failed", Stale: true, LastChecked: time.Now().UTC().Format(time.RFC3339)}
			var release struct {
				Tag        string `json:"tag_name"`
				Draft      bool   `json:"draft"`
				Prerelease bool   `json:"prerelease"`
			}
			if err := c.metadata(ctx, p.Repository, "/releases/latest", &release); err != nil {
				p.Error = err.Error()
				checked <- p
				return
			}
			if release.Draft || release.Prerelease || !tagPattern.MatchString(release.Tag) {
				p.Error = "No supported stable release tag"
				checked <- p
				return
			}
			var commit struct {
				SHA string `json:"sha"`
			}
			if err := c.metadata(ctx, p.Repository, "/commits/"+url.PathEscape(release.Tag), &commit); err != nil {
				p.Error = err.Error()
				checked <- p
				return
			}
			if !commitPattern.MatchString(commit.SHA) {
				p.Error = "GitHub commit identity is invalid"
				checked <- p
				return
			}
			p.LatestVersion = release.Tag
			p.LatestCommit = commit.SHA
			p.Stale = false
			p.State = "up_to_date"
			if commit.SHA != current.Commit {
				compared, err := product.CompareReleaseVersions(release.Tag, current.Version)
				if err != nil || compared < 0 {
					p.State = "different_release"
					p.Error = "Latest stable tag is not a newer supported version; no automatic downgrade"
				} else {
					p.State = "update_available"
				}
			}
			checked <- p
		}(id)
	}
	projects := map[string]Project{}
	for range projectOrder {
		p := <-checked
		projects[p.ID] = p
	}
	resolver := c.toolResolver
	if resolver == nil {
		resolver = resolveTools
	}
	_, toolStatus := resolver(ctx, c.opts.ToolsFile)
	reason := ""
	enabled := c.opts.Enabled
	if !enabled {
		reason = "Direct source installation disabled. Start with --web-updates --source-updates to explicitly trust upstream build code."
	}
	if enabled && !toolStatus.Ready {
		enabled = false
		reason = "Build tools are unavailable. Reinstall the complete package including build-tools and update-tools.json, or configure trusted custom SDK paths."
	}
	if enabled {
		if err := checkKitHeader(c.opts); err != nil {
			enabled = false
			reason = "The packaged source recipe does not match this running binary; reinstall its matching updater-source directory."
		}
	}
	if enabled && c.maintenance != nil {
		state := c.maintenance.Snapshot()
		if state["enabled"] != true {
			enabled = false
			reason = "Portable maintenance is disabled; review its deployment/path restrictions."
		}
	}
	c.mu.Lock()
	c.status.Enabled = enabled
	c.status.Reason = reason
	c.status.Tools = toolStatus
	c.status.Projects = nil
	for _, id := range projectOrder {
		c.status.Projects = append(c.status.Projects, projects[id])
	}
	c.mu.Unlock()
	return c.Snapshot()
}
func (c *Controller) Run(ctx context.Context) {
	c.Check(ctx)
	timer := time.NewTicker(6 * time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			c.Check(ctx)
		}
	}
}
func Confirmation(targets map[string]webupdate.SourceTarget) string {
	parts := []string{}
	for _, id := range projectOrder {
		if target, ok := targets[id]; ok {
			parts = append(parts, projectNames[id]+" "+target.Version)
		}
	}
	return strings.Join(parts, " + ")
}
func (c *Controller) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	root := "/plus-api/releases/upstreams"
	if r.URL.Path == root && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(c.Snapshot())
		return
	}
	if r.Method != http.MethodPost || (r.URL.Path != root+"/check" && r.URL.Path != root+"/install") {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, `{"error":"cross-site source action rejected"}`, 403)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, r.Host) {
			http.Error(w, `{"error":"cross-origin source action rejected"}`, 403)
			return
		}
	}
	if r.URL.Path == root+"/check" {
		_ = json.NewEncoder(w).Encode(c.Check(r.Context()))
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, `{"error":"application/json required"}`, 415)
		return
	}
	var input struct {
		Project         string            `json:"project"`
		Targets         map[string]string `json:"targets"`
		Confirmation    string            `json:"confirmation"`
		TrustSourceCode bool              `json:"trust_source_code"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		http.Error(w, `{"error":"invalid source update request"}`, 400)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, `{"error":"one JSON object required"}`, 400)
		return
	}
	if input.Project != "all" && input.Project != "cliproxyapi" && input.Project != "cpa-manager-plus" {
		http.Error(w, `{"error":"choose one or both known upstream projects"}`, 400)
		return
	}
	status := c.Check(r.Context())
	if !status.Enabled || !input.TrustSourceCode || c.maintenance == nil {
		http.Error(w, `{"error":"source maintenance disabled or source-code execution consent missing"}`, 409)
		return
	}
	targets := map[string]webupdate.SourceTarget{}
	changed := false
	for _, project := range status.Projects {
		if input.Project != "all" && input.Project != project.ID {
			continue
		}
		if project.Stale || (project.State != "up_to_date" && project.State != "update_available") || input.Targets[project.ID] != project.LatestCommit {
			http.Error(w, `{"error":"upstream release changed or check failed; refresh before confirming"}`, 409)
			return
		}
		targets[project.ID] = webupdate.SourceTarget{Version: project.LatestVersion, Commit: project.LatestCommit}
		changed = changed || project.State == "update_available"
	}
	if !changed || len(input.Targets) != len(targets) || input.Confirmation != Confirmation(targets) {
		http.Error(w, `{"error":"confirm the exact checked upstream versions and commits"}`, 409)
		return
	}
	builder := c.builder
	if builder == nil {
		builder = c.build
	}
	op, err := c.maintenance.StartSource(webupdate.SourceRequest{Targets: targets, Current: c.opts.Current}, builder)
	if err != nil {
		http.Error(w, `{"error":"source maintenance could not start; another operation, recovery or installation restriction may be active"}`, 409)
		return
	}
	w.WriteHeader(202)
	_ = json.NewEncoder(w).Encode(op)
}
