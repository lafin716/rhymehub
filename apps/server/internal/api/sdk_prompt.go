package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/lafin716/rhymehub/apps/server/sdkdist"
	"github.com/lafin716/rhymehub/templates"
)

// sdkSkillPath is where the dashboard tells users to save the rendered skill in their app repository.
const sdkSkillPath = ".claude/skills/rhymehub-sdk/SKILL.md"

// sdkDistPath is the public download prefix for the embedded SDK tarballs.
const sdkDistPath = "/api/v1/sdk/dist/"

// hostRE accepts a DNS name or IPv4 address, or a bracketed IPv6 address, with an optional port. It keeps request
// header text from leaking odd characters into the rendered prompt.
var hostRE = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9.-]{0,252})|\[[0-9A-Fa-f:.]{2,45}\])(?::[0-9]{1,5})?$`)

var sdkSkillTmpl = template.Must(template.ParseFS(templates.FS, "sdk-widget-skill.md"))

type sdkPromptPackage struct {
	Name string `json:"name"`
	File string `json:"file"`
	URL  string `json:"url"`
}

// handleSDKPrompt serves GET /projects/{id}/sdk-prompt: an agent prompt (and the same text as a Claude Code
// skill) that installs the SDK and feedback widget into the project's app. It contains no keys or tokens.
func (s *Server) handleSDKPrompt(w http.ResponseWriter, r *http.Request, _ *principal) {
	project, err := s.st.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	serverURL, err := promptServerURL(r)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	pkgs := make([]sdkPromptPackage, 0, 3)
	for _, p := range sdkdist.Packages() {
		pkgs = append(pkgs, sdkPromptPackage{Name: p.Name, File: p.File, URL: serverURL + sdkDistPath + p.File})
	}
	var body bytes.Buffer
	if err := sdkSkillTmpl.Execute(&body, map[string]any{
		"ProjectID":  project.ID,
		"ServerURL":  serverURL,
		"SDKVersion": sdkdist.Version(),
		"Packages":   pkgs,
	}); err != nil {
		writeDomainError(w, fmt.Errorf("render sdk skill: %w", err))
		return
	}
	prompt := "Install the rhymehub SDK and feedback widget into this repository by following these instructions.\n\n" +
		body.String()
	skill := "---\nname: rhymehub-sdk\ndescription: Install the rhymehub SDK and in-app feedback widget for rhymehub project " +
		strconv.Quote(project.ID) + ". Use when asked to add rhymehub, the rhymehub SDK or the feedback widget.\n---\n\n" +
		body.String()
	writeJSON(w, http.StatusOK, map[string]any{
		"projectId":    project.ID,
		"serverUrl":    serverURL,
		"sdkVersion":   sdkdist.Version(),
		"sdkAvailable": sdkdist.Available(),
		"packages":     pkgs,
		"prompt":       prompt,
		"skill":        skill,
		"skillPath":    sdkSkillPath,
	})
}

// promptServerURL is the rhymehub base URL the app should use: the ?serverUrl= query when given (the dashboard
// lets the user pick a LAN or Tailscale address), otherwise the scheme and host of this request.
func promptServerURL(r *http.Request) (string, error) {
	if raw := strings.TrimSpace(r.URL.Query().Get("serverUrl")); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !hostRE.MatchString(u.Host) ||
			u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", badRequest("serverUrl must be an http(s) origin such as https://host.example.ts.net:7780")
		}
		return u.Scheme + "://" + u.Host, nil
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if !hostRE.MatchString(host) {
		host = "127.0.0.1:7780"
	}
	return scheme + "://" + host, nil
}

// handleSDKDist serves GET /api/v1/sdk/dist/{file}: the embedded MIT-licensed SDK tarballs. It is public, like the
// npm registry would be, and serves only file names that match the tarball pattern.
func (s *Server) handleSDKDist(w http.ResponseWriter, r *http.Request) {
	b, ok := sdkdist.Lookup(r.PathValue("file"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such SDK tarball")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}
