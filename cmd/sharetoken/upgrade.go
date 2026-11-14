package main

// sharetoken upgrade: fetches the newest release's signed package for the way
// this program was installed, checks it against the release key, and installs
// it (internal/client/upgrade.go, docs/versioning.md "一键升级").

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"sharetoken/internal/cli"
	"sharetoken/internal/client"
)

func (c *cmdCtx) upgrade(args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	yes := fs.Bool("y", false, "do not ask before installing")
	_ = fs.Parse(args)
	inst := client.DetectInstallation()
	base, pin := client.PortalAddr(), ""
	if c.cfg.Portal != "" {
		base, pin = c.cfg.Portal, c.cfg.PortalPin
	}
	cx, cancel := ctx()
	u, err := client.CheckUpdate(cx, base, pin, inst.UpdateKind())
	cancel()
	if err != nil {
		return fmt.Errorf("asking %s for the current release: %w", base, err)
	}
	if !u.Available {
		fmt.Println("up to date")
		return nil
	}
	printUpdate(u)
	if u.Install == nil {
		return errors.New("this release has no signed package that this copy can install by itself; download it from the address above")
	}
	if inst.Kind == "macos-app" {
		if running := appRunning(inst.Path); running {
			return errors.New("the desktop app is running: update it from the app (it offers the update and restarts itself), or quit it and run this again")
		}
	}
	fmt.Printf("installed as: %s (%s)\n", describeInstallation(inst), inst.Path)
	if !*yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("run it with -y to install without asking")
		}
		ans, err := cli.Prompt("Install " + u.Latest + " now? [y/N] ")
		if err != nil || !strings.EqualFold(strings.TrimSpace(ans), "y") {
			return errors.New("not installed")
		}
	}
	hc := client.NewPortalClient(base, "", pin).HTTP
	hc.Timeout = 0
	dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer dcancel()
	tty := term.IsTerminal(int(os.Stdout.Fd()))
	path, err := client.DownloadPackage(dctx, hc, u.Install, filepath.Join(c.home, client.UpdateDir), func(written, total int64) {
		if tty && total > 0 {
			fmt.Printf("\rfetching %s: %d%%", u.Install.File, written*100/total)
		}
	})
	if tty {
		fmt.Println()
	}
	if err != nil {
		return err
	}
	fmt.Printf("fetched %s; checksum and release signature verified\n", u.Install.File)
	switch inst.Kind {
	case "cli":
		return installCLI(path, inst.Path)
	case "macos-app":
		return installMacApp(path, inst.Path)
	case "windows-app":
		// The installer stops the app and every sharetoken program in its
		// directory, this one too, so it runs on its own once this has left.
		if err := startDetached(path, "/S"); err != nil {
			return fmt.Errorf("starting the installer: %w", err)
		}
		fmt.Println("the installer runs in the background: it closes the app and the service, installs " + u.Latest + ", and is done in a few seconds; start the app again afterwards")
		return nil
	case "linux-deb":
		return installDeb(path)
	}
	return fmt.Errorf("unknown installation %s", inst.Kind)
}

// removeReplacedProgram deletes what `upgrade` leaves on Windows: the program
// it replaced, renamed to .old because it was running.
func removeReplacedProgram() {
	if runtime.GOOS != "windows" {
		return
	}
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

func describeInstallation(i client.Installation) string {
	return map[string]string{
		"cli":         "the command-line program",
		"macos-app":   "the macOS app",
		"windows-app": "the Windows app",
		"linux-deb":   "the Debian/Ubuntu package",
	}[i.Kind]
}

// installCLI puts the program of the archive in place of exe. A running
// program keeps running the old one until it is restarted.
func installCLI(archive, exe string) error {
	dir := filepath.Dir(exe)
	next := exe + ".new"
	if err := extractProgram(archive, next); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%s cannot be written by this user: run the upgrade as its owner, or install the package by hand", dir)
		}
		return err
	}
	if runtime.GOOS == "windows" {
		// A running program cannot be replaced on Windows, but it can be renamed.
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			os.Remove(next)
			return err
		}
		if err := os.Rename(next, exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
	} else if err := os.Rename(next, exe); err != nil {
		os.Remove(next)
		return err
	}
	out, err := exec.Command(exe, "version").Output()
	if err != nil {
		return fmt.Errorf("the new program does not start: %w", err)
	}
	fmt.Printf("installed: %s\n", strings.SplitN(string(out), "\n", 2)[0])
	fmt.Println("a running `sharetoken up` or `sharetoken daemon` keeps the old version until it is restarted")
	return nil
}

// extractProgram writes the sharetoken program of a release archive to dst.
func extractProgram(archive, dst string) error {
	want := func(name string) bool {
		b := strings.TrimSuffix(filepath.Base(name), ".exe")
		return strings.HasPrefix(b, "sharetoken") && !strings.Contains(b, ".")
	}
	write := func(r io.Reader) error {
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(r, 512<<20)); err != nil {
			f.Close()
			os.Remove(dst)
			return err
		}
		return f.Close()
	}
	if strings.HasSuffix(archive, ".zip") {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if f.Mode().IsRegular() && want(f.Name) {
				r, err := f.Open()
				if err != nil {
					return err
				}
				defer r.Close()
				return write(r)
			}
		}
		return errors.New("the archive has no sharetoken program")
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return errors.New("the archive has no sharetoken program")
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && want(h.Name) {
			return write(tr)
		}
	}
}

// installMacApp swaps the app bundle for the one in zip; the app is not running.
func installMacApp(zipPath, bundle string) error {
	parent := filepath.Dir(bundle)
	stage, err := os.MkdirTemp(parent, ".sharetoken-update-")
	if err != nil {
		return fmt.Errorf("%s cannot be written by this user: %w", parent, err)
	}
	defer os.RemoveAll(stage)
	if out, err := exec.Command("ditto", "-x", "-k", zipPath, stage).CombinedOutput(); err != nil {
		return fmt.Errorf("unpacking: %v: %s", err, out)
	}
	entries, err := os.ReadDir(stage)
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".app") {
		return errors.New("the package does not hold one app")
	}
	app := filepath.Join(stage, entries[0].Name())
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		return fmt.Errorf("the new app's code signature does not verify: %s", out)
	}
	backup := bundle + ".old"
	_ = os.RemoveAll(backup)
	if err := os.Rename(bundle, backup); err != nil {
		return err
	}
	if err := os.Rename(app, bundle); err != nil {
		_ = os.Rename(backup, bundle)
		return err
	}
	_ = os.RemoveAll(backup)
	fmt.Printf("installed %s\n", bundle)
	return nil
}

// appRunning says whether a program of the bundle is running, this one aside.
func appRunning(bundle string) bool {
	out, _ := exec.Command("pgrep", "-f", filepath.Join(bundle, "Contents", "MacOS", "")).Output()
	for _, f := range strings.Fields(string(out)) {
		if f != fmt.Sprint(os.Getpid()) {
			return true
		}
	}
	return false
}

// installDeb installs the package with dpkg, as root: with sudo in a terminal,
// else through pkexec, which asks in the desktop.
func installDeb(deb string) error {
	var cmd *exec.Cmd
	switch {
	case os.Geteuid() == 0:
		cmd = exec.Command("dpkg", "-i", deb)
	case term.IsTerminal(int(os.Stdin.Fd())) && hasProgram("sudo"):
		cmd = exec.Command("sudo", "dpkg", "-i", deb)
	case hasProgram("pkexec"):
		cmd = exec.Command("pkexec", "dpkg", "-i", deb)
	default:
		return fmt.Errorf("installing needs root: run  sudo dpkg -i %s", deb)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dpkg: %w", err)
	}
	fmt.Println("installed; a running app or `sharetoken up` keeps the old version until it is restarted")
	return nil
}

func hasProgram(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
