package supervisor

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// LaunchdLabel is the launchd job label for the supervisor.
const LaunchdLabel = "dev.rhymehub.supervisor"

// LaunchdOptions describes the launchd job to install.
type LaunchdOptions struct {
	Home   Home   // must have an absolute Root
	Binary string // absolute path to the rhymehub-supervisor executable
	Dest   string // plist path; defaults to ~/Library/LaunchAgents/<label>.plist
	Load   bool   // run `launchctl bootstrap gui/<uid>` after writing the plist
	UID    int    // gui domain uid; defaults to the current uid
}

// RenderLaunchdPlist returns the launchd plist for the supervisor. All values are XML-escaped.
func RenderLaunchdPlist(opts LaunchdOptions) ([]byte, error) {
	if !filepath.IsAbs(opts.Binary) {
		return nil, fmt.Errorf("launchd: binary path must be absolute, got %q", opts.Binary)
	}
	if !filepath.IsAbs(opts.Home.Root) {
		return nil, fmt.Errorf("launchd: home path must be absolute, got %q", opts.Home.Root)
	}
	logOut := filepath.Join(opts.Home.LogsDir(), "supervisor.out.log")
	logErr := filepath.Join(opts.Home.LogsDir(), "supervisor.err.log")

	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	writeKeyString(&b, "Label", LaunchdLabel)
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range []string{opts.Binary, "run", "--home", opts.Home.Root} {
		b.WriteString("    <string>")
		if err := xml.EscapeText(&b, []byte(arg)); err != nil {
			return nil, err
		}
		b.WriteString("</string>\n")
	}
	b.WriteString("  </array>\n")
	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	b.WriteString("    <key>RHYMEHUB_HOME</key>\n    <string>")
	if err := xml.EscapeText(&b, []byte(opts.Home.Root)); err != nil {
		return nil, err
	}
	b.WriteString("</string>\n  </dict>\n")
	writeKeyString(&b, "WorkingDirectory", opts.Home.Root)
	writeKeyString(&b, "StandardOutPath", logOut)
	writeKeyString(&b, "StandardErrorPath", logErr)
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	b.WriteString("  <key>KeepAlive</key>\n  <true/>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes(), nil
}

func writeKeyString(b *bytes.Buffer, key, value string) {
	b.WriteString("  <key>" + key + "</key>\n  <string>")
	_ = xml.EscapeText(b, []byte(value))
	b.WriteString("</string>\n")
}

// InstallLaunchd writes the plist atomically and, when opts.Load is set, bootstraps it into the
// user's gui domain. It returns the plist path.
func InstallLaunchd(opts LaunchdOptions) (string, error) {
	data, err := RenderLaunchdPlist(opts)
	if err != nil {
		return "", err
	}
	dest := opts.Dest
	if dest == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dest = filepath.Join(userHome, "Library", "LaunchAgents", LaunchdLabel+".plist")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(opts.Home.LogsDir(), 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(dest, data, 0o644); err != nil {
		return "", err
	}
	if !opts.Load {
		return dest, nil
	}
	uid := opts.UID
	if uid == 0 {
		uid = os.Getuid()
	}
	out, err := exec.Command("launchctl", "bootstrap", "gui/"+strconv.Itoa(uid), dest).CombinedOutput()
	if err != nil {
		return dest, fmt.Errorf("launchctl bootstrap: %w: %s", err, tailText(out))
	}
	return dest, nil
}

// writeFileAtomic writes data through a temp file, fsync and rename, then syncs the directory.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return errors.Join(err, os.Remove(tmp))
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return errors.Join(err, os.Remove(tmp))
	}
	if err := f.Close(); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	return syncDir(filepath.Dir(path))
}
