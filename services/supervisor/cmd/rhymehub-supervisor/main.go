// Command rhymehub-supervisor runs and manages the rhymehub core process.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lafin716/rhymehub/services/supervisor"
)

const usage = `usage: rhymehub-supervisor <command> [flags]

commands:
  run               run the supervisor in the foreground
  status            print the supervisor status from the loopback API
  install-launchd   write the launchd plist (use --load to bootstrap it)
  bootstrap         create the home layout, API token and default config
  keygen            create the release signing key pair
  sign-release      sign an artifact and record it in a feed manifest
  help              show this help

Every command accepts --home (default $RHYMEHUB_HOME, then ~/.rhymehub).
`

// version is set at build time with -X main.version.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "run":
		err = cmdRun(args)
	case "status":
		err = cmdStatus(args)
	case "install-launchd":
		err = cmdInstallLaunchd(args)
	case "bootstrap":
		err = cmdBootstrap(args)
	case "keygen":
		err = cmdKeygen(args)
	case "sign-release":
		err = cmdSignRelease(args)
	case "version":
		fmt.Println("rhymehub-supervisor", version)
		return
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// newFlags builds a FlagSet with the shared --home flag.
func newFlags(name string, home *string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(home, "home", "", "RHYMEHUB_HOME directory")
	return fs
}

func resolveHome(flagValue string) (supervisor.Home, error) {
	h, err := supervisor.ResolveHome(flagValue)
	if err != nil {
		return supervisor.Home{}, err
	}
	abs, err := filepath.Abs(h.Root)
	if err != nil {
		return supervisor.Home{}, err
	}
	return supervisor.Home{Root: abs}, nil
}

func cmdRun(args []string) error {
	var homeFlag, feed string
	fs := newFlags("run", &homeFlag)
	fs.StringVar(&feed, "feed", "", "update feed URL (overrides config.json updateFeed)")
	addr := fs.String("addr", supervisor.DefaultSupervisorAddr, "loopback API address")
	allowUnsigned := fs.Bool("allow-unsigned", false, "dev only: accept unsigned artifacts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := resolveHome(homeFlag)
	if err != nil {
		return err
	}
	sup, err := supervisor.New(supervisor.Options{
		Home:           h,
		FeedURL:        feed,
		AllowUnsigned:  *allowUnsigned,
		SupervisorAddr: *addr,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return sup.Run(ctx)
}

func cmdStatus(args []string) error {
	var homeFlag string
	fs := newFlags("status", &homeFlag)
	addr := fs.String("addr", supervisor.DefaultSupervisorAddr, "loopback API address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := resolveHome(homeFlag)
	if err != nil {
		return err
	}
	tok, err := os.ReadFile(h.TokenPath())
	if err != nil {
		return fmt.Errorf("read supervisor token: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "http://"+*addr+"/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("supervisor not reachable at %s: %w", *addr, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var pretty any
	if err := json.Unmarshal(body, &pretty); err != nil {
		return err
	}
	out, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func cmdInstallLaunchd(args []string) error {
	var homeFlag, dest, binary string
	fs := newFlags("install-launchd", &homeFlag)
	fs.StringVar(&dest, "dest", "", "plist path (default ~/Library/LaunchAgents/dev.rhymehub.supervisor.plist)")
	fs.StringVar(&binary, "binary", "", "absolute path to this supervisor binary (default: the running executable)")
	load := fs.Bool("load", false, "run launchctl bootstrap gui/<uid> after writing the plist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := resolveHome(homeFlag)
	if err != nil {
		return err
	}
	if binary == "" {
		binary, err = os.Executable()
		if err != nil {
			return err
		}
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	path, err := supervisor.InstallLaunchd(supervisor.LaunchdOptions{
		Home:   h,
		Binary: binary,
		Dest:   dest,
		Load:   *load,
	})
	if err != nil {
		return err
	}
	fmt.Println("wrote", path)
	if !*load {
		fmt.Println("not loaded; re-run with --load to bootstrap the launchd job")
	}
	return nil
}

func cmdBootstrap(args []string) error {
	var homeFlag string
	fs := newFlags("bootstrap", &homeFlag)
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := resolveHome(homeFlag)
	if err != nil {
		return err
	}
	if err := supervisor.Bootstrap(h); err != nil {
		return err
	}
	fmt.Println("home ready:", h.Root)
	return nil
}

func cmdKeygen(args []string) error {
	var homeFlag string
	fs := newFlags("keygen", &homeFlag)
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := resolveHome(homeFlag)
	if err != nil {
		return err
	}
	pub, err := supervisor.Keygen(h)
	if err != nil {
		return err
	}
	fmt.Println("private key:", h.PrivateKeyPath(), "(keep secret)")
	fmt.Println("public key: ", h.PublicKeyPath())
	fmt.Printf("public key (base64): %s\n", base64.StdEncoding.EncodeToString(pub))
	return nil
}

func cmdSignRelease(args []string) error {
	var homeFlag, keyPath, name string
	var opts supervisor.SignReleaseOptions
	fs := newFlags("sign-release", &homeFlag)
	fs.StringVar(&opts.Version, "version", "", "release version (required)")
	fs.StringVar(&opts.Artifact, "artifact", "", "path to the tarball (required)")
	fs.StringVar(&opts.OS, "os", "", "target GOOS (required)")
	fs.StringVar(&opts.Arch, "arch", "", "target GOARCH (required)")
	fs.StringVar(&opts.URL, "url", "", "artifact URL as published (required)")
	fs.StringVar(&opts.Manifest, "manifest", "", "feed manifest path (required)")
	fs.StringVar(&keyPath, "key", "", "private key path (default secrets/release-signing.key)")
	fs.StringVar(&name, "name", "rhymehub", "product name for a new manifest")
	fs.StringVar(&opts.PublishedAt, "published-at", time.Now().UTC().Format(time.RFC3339), "publishedAt timestamp")
	fs.StringVar(&opts.Notes, "notes", "", "release notes")
	fs.IntVar(&opts.SchemaVersion, "schema-version", 0, "schema version of this release")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := resolveHome(homeFlag)
	if err != nil {
		return err
	}
	opts.Home = h
	opts.KeyPath = keyPath
	opts.Name = name
	res, err := supervisor.SignRelease(opts)
	if err != nil {
		return err
	}
	fmt.Println("sha256:   ", res.Digest)
	fmt.Println("signature:", res.Signature)
	fmt.Println("manifest: ", res.Manifest)
	return nil
}
