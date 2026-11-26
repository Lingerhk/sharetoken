package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"sharetoken/internal/client"
	"sharetoken/internal/diag"
)

// logs prints the client's log, or where it is.
func (c *cmdCtx) logs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	n := fs.Int("n", 200, "how many of the last lines")
	follow := fs.Bool("f", false, "keep printing new lines")
	gui := fs.Bool("gui", false, "the desktop app's own log instead of the client's")
	_ = fs.Parse(args)
	dir := client.LogDir(c.home, c.logDir, c.cfg)
	if fs.Arg(0) == "path" {
		fmt.Println(dir)
		return nil
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unknown argument %q (want: logs [-n N] [-f] [-gui] | logs path)", fs.Arg(0))
	}
	name := diag.ClientLog
	if *gui {
		name = diag.GUILog
	}
	p := filepath.Join(dir, name)
	lines := diag.Tail(p, *n)
	if len(lines) == 0 && !*follow {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("no log at %s yet: it is written by `sharetoken up`, `sharetoken daemon` and the desktop app", p)
		}
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	if !*follow {
		return nil
	}
	return followFile(p)
}

// followFile prints what is appended to p, following a rotation.
func followFile(p string) error {
	var f *os.File
	var off int64
	open := func() {
		if f != nil {
			f.Close()
		}
		f, off = nil, 0
		if g, err := os.Open(p); err == nil {
			f = g
		}
	}
	open()
	if f != nil {
		off, _ = f.Seek(0, io.SeekEnd)
	}
	for {
		time.Sleep(500 * time.Millisecond)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if f == nil || st.Size() < off { // rotated, or created
			open()
		}
		if f == nil {
			continue
		}
		r := bufio.NewReader(f)
		for {
			line, err := r.ReadString('\n')
			off += int64(len(line))
			if line != "" {
				fmt.Print(line)
			}
			if err != nil {
				break
			}
		}
	}
}

// diagnostics shows or changes whether crash reports are sent.
func (c *cmdCtx) diagnostics(args []string) error {
	dir := client.LogDir(c.home, c.logDir, c.cfg)
	q := diag.Queue{Dir: filepath.Join(dir, diag.ReportsDir)}
	switch {
	case len(args) == 1 && (args[0] == "on" || args[0] == "off"):
		on := args[0] == "on"
		if err := client.SetReportUpload(c.home, on, q); err != nil {
			return err
		}
		if on {
			fmt.Println("crash reports: on. A crash or panic is sent to the portal, redacted, with your login.")
		} else {
			fmt.Println("crash reports: off. Nothing is sent; the queue was emptied.")
		}
		return nil
	case len(args) == 1 && args[0] == "show":
		reps, err := q.List()
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(reps, "", "  ")
		fmt.Println(string(data))
		return nil
	case len(args) > 0:
		return errors.New("usage: sharetoken diagnostics [on|off|show]")
	}
	cfg, err := client.LoadConfig(c.home)
	if err != nil {
		return err
	}
	reps, _ := q.List()
	state := "off"
	if cfg.UploadReports() {
		state = "on"
	}
	how := "your choice"
	if cfg.Diagnostics.Upload == nil {
		how = "the default"
		if r := diag.SystemRegion(); r.EEA {
			how += fmt.Sprintf(": off in the European Economic Area (country %q, time zone %q)", r.Country, r.Zone)
		}
	}
	fmt.Printf("crash reports: %s (%s)\n", state, how)
	r := diag.SystemRegion()
	fmt.Printf("region: country %q, time zone %q, European Economic Area: %v\n", r.Country, r.Zone, r.EEA)
	fmt.Printf("waiting to be sent: %d (sharetoken diagnostics show)\n", len(reps))
	fmt.Printf("log directory: %s\n", dir)
	return nil
}
