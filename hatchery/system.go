package hatchery

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/uc-cdis/hatchery/hatchery/version"
)

type versionSummary struct {
	Commit  string `json:"commit"`
	Version string `json:"version"`
}

type debugData struct {
	UserNamespace    string
	SubDir           string
	UserVolumeSize   string
	SkipNodeSelector bool
	UseInternalURL   bool
	HashedUsernames  bool
	Containers       []debugContainer
	Sidecar          debugSidecar
	OptionalSections map[string]bool
}

type debugContainer struct {
	Name        string
	Image       string
	Port        int32
	CPU         string
	Memory      string
	EnvKeys     string
	HasAuthz    bool
	HasNextflow bool
	HasLicense  bool
	HasSquashFS bool
}

type debugSidecar struct {
	Image   string
	CPU     string
	Memory  string
	EnvKeys string
}

const debugFallback = `=== Hatchery Debug Config ===

user-namespace:     {{ .UserNamespace }}
sub-dir:            {{ .SubDir }}
user-volume-size:   {{ .UserVolumeSize }}
skip-node-selector: {{ .SkipNodeSelector }}
use-internal-url:   {{ .UseInternalURL }}
hashed-usernames:   {{ .HashedUsernames }}

--- Sidecar ---
image:    {{ .Sidecar.Image }}
cpu:      {{ .Sidecar.CPU }}
memory:   {{ .Sidecar.Memory }}
env-keys: {{ .Sidecar.EnvKeys }}

--- Containers ({{ len .Containers }}) ---
{{ range .Containers -}}
[{{ .Name }}]
  image:    {{ .Image }}
  port:     {{ .Port }}
  cpu:      {{ .CPU }}  memory: {{ .Memory }}
  env-keys: {{ .EnvKeys }}
  authz: {{ .HasAuthz }}  nextflow: {{ .HasNextflow }}  license: {{ .HasLicense }}  squashfs: {{ .HasSquashFS }}

{{ end -}}
--- Optional Sections ---
{{ range $k, $v := .OptionalSections -}}
{{ $k }}: {{ if $v }}ENABLED{{ else }}disabled{{ end }}
{{ end -}}
`

var debugTmpl *template.Template

// loadTextTemplate reads name from /var/hatchery/templates/ (production) or
// ./templates/ (dev), falling back to the inline fallback string.
func loadTextTemplate(name, fallback string) *template.Template {
	for _, dir := range []string{"/var/hatchery/templates", "templates"} {
		path := filepath.Join(dir, name)
		if content, err := os.ReadFile(path); err == nil {
			if t, err := template.New(name).Parse(string(content)); err == nil {
				return t
			}
		}
	}
	return template.Must(template.New(name).Parse(fallback))
}

func RegisterSystem() {
	debugTmpl = loadTextTemplate("debug.txt", debugFallback)
	http.HandleFunc("/_status", systemStatus)
	http.HandleFunc("/_version", systemVersion)
	http.HandleFunc("/_debug", systemDebug)
}

func systemStatus(w http.ResponseWriter, r *http.Request) {
	if _, err := fmt.Fprintf(w, "Healthy"); err != nil {
		Config.Logger.Printf("Error writing system status response: %v", err)
	}
}

func systemVersion(w http.ResponseWriter, r *http.Request) {
	ver := versionSummary{Commit: version.GitCommit, Version: version.GitVersion}
	out, err := json.Marshal(ver)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if _, err = fmt.Fprint(w, string(out)); err != nil {
		Config.Logger.Printf("Error writing status response: %v", err)
	}
}

func systemDebug(w http.ResponseWriter, r *http.Request) {
	cfg := Config.Config

	sortedEnvKeys := func(env map[string]string) string {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return strings.Join(keys, ", ")
	}

	data := debugData{
		UserNamespace:    cfg.UserNamespace,
		SubDir:           cfg.SubDir,
		UserVolumeSize:   cfg.UserVolumeSize,
		SkipNodeSelector: cfg.SkipNodeSelector,
		UseInternalURL:   cfg.UseInteralServicesURL,
		HashedUsernames:  cfg.HashedUsernames,
		OptionalSections: map[string]bool{
			"s3-config":              cfg.S3Config.BucketName != "",
			"nextflow-global-config": cfg.NextflowGlobalConfig.SampleConfigPublicImage != "",
			"prisma-config":          cfg.PrismaConfig.Enable,
			"license":                cfg.License.Enabled,
			"more-configs":           len(cfg.MoreConfigs) > 0,
			"pricing":                cfg.Pricing.Cpu != 0 || cfg.Pricing.Memory != 0,
			"pay-models":             len(cfg.PayModels) > 0,
			"shared-workspace":       cfg.SharedWorkspace.Enabled,
		},
		Sidecar: debugSidecar{
			Image:   cfg.Sidecar.Image,
			CPU:     cfg.Sidecar.CPULimit,
			Memory:  cfg.Sidecar.MemoryLimit,
			EnvKeys: sortedEnvKeys(cfg.Sidecar.Env),
		},
	}

	for _, c := range Config.ContainersMap {
		data.Containers = append(data.Containers, debugContainer{
			Name:        c.Name,
			Image:       c.Image,
			Port:        c.TargetPort,
			CPU:         c.CPULimit,
			Memory:      c.MemoryLimit,
			EnvKeys:     sortedEnvKeys(c.Env),
			HasAuthz:    c.Authz.Version != 0,
			HasNextflow: c.NextflowConfig.Enabled,
			HasLicense:  c.License.Enabled,
			HasSquashFS: c.SquashFSMount.Enabled,
		})
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := debugTmpl.Execute(w, data); err != nil {
		Config.Logger.Printf("Error rendering debug template: %v", err)
	}
}
