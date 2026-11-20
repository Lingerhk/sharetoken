package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"sharetoken/internal/cli"
	"sharetoken/internal/provider"
	"sharetoken/internal/warmup"
)

// warmup: start Claude and ChatGPT (Codex) usage windows at times of day,
// for the agents of this machine and the subscription logins sold
// (internal/warmup). Settings are files, so they are changed with the
// client stopped too; warm-ups happen while it runs.
func (c *cmdCtx) warmup(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "status", "ls":
		return c.warmupStatus()
	case "set":
		return c.warmupSet(args)
	case "run", "now":
		return c.warmupRun(args)
	case "pause", "resume":
		s, err := warmup.LoadSettings(c.home)
		if err != nil {
			return err
		}
		s.Paused = sub == "pause"
		if err := warmup.SaveSettings(c.home, s); err != nil {
			return err
		}
		return c.warmupStatus()
	default:
		return fmt.Errorf("unknown warmup command %q", sub)
	}
}

// warmupFlags are the settings a warm-up has, as flags.
type warmupFlags struct {
	times, onReset, model *string
	off                   *bool
}

func addWarmupFlags(fs *flag.FlagSet) warmupFlags {
	return warmupFlags{
		times:   fs.String("times", "", "local times of day to start the 5-hour window at, e.g. 06:00,11:05 (\"none\" for none)"),
		onReset: fs.String("on-reset", "", "start windows as soon as they reset: off, week (the weekly window) or all (the 5-hour one too)"),
		model:   fs.String("model", "", "model to ask (\"default\" for the cheapest)"),
		off:     fs.Bool("off", false, "turn the warm-up off"),
	}
}

func (f warmupFlags) apply(fs *flag.FlagSet, times *[]string, onReset, model *string) {
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "times":
			*times = nil
			if v := strings.TrimSpace(*f.times); v != "none" && v != "" {
				*times = cli.List(v)
			}
		case "on-reset":
			*onReset = strings.TrimSpace(*f.onReset)
			if *onReset == "off" {
				*onReset = ""
			}
		case "model":
			*model = strings.TrimSpace(*f.model)
			if *model == "default" {
				*model = ""
			}
		}
	})
}

func (c *cmdCtx) warmupSet(args []string) error {
	usage := errors.New("usage: sharetoken warmup set claude|codex [-times 06:00,11:05] [-on-reset off|week|all] [-model M] [-binary PATH] [-off]")
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usage
	}
	agent := strings.TrimPrefix(strings.ToLower(args[0]), "local:")
	fs := flag.NewFlagSet("warmup set", flag.ExitOnError)
	wf := addWarmupFlags(fs)
	bin := fs.String("binary", "", "the agent's program (\"auto\": found by itself)")
	_ = fs.Parse(args[1:])
	s, err := warmup.LoadSettings(c.home)
	if err != nil {
		return err
	}
	var l *warmup.Local
	switch agent {
	case warmup.AgentClaude:
		l = &s.Claude
	case warmup.AgentCodex:
		l = &s.Codex
	default:
		return usage
	}
	wf.apply(fs, &l.Times, &l.OnReset, &l.Model)
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "binary" {
			l.Binary = strings.TrimSpace(*bin)
			if l.Binary == "auto" {
				l.Binary = ""
			}
		}
	})
	l.Enabled = !*wf.off
	if l.Enabled && len(l.Times) == 0 && l.OnReset == "" {
		return errors.New("give -times or -on-reset, or -off")
	}
	if err := warmup.SaveSettings(c.home, s); err != nil {
		return err
	}
	return c.warmupStatus()
}

// providerWarmup sets a seller provider's warm-up.
func (c *cmdCtx) providerWarmup(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: sharetoken provider warmup <name> [-times 06:00,11:05] [-on-reset off|week|all] [-model M] [-off]")
	}
	name := args[0]
	fs := flag.NewFlagSet("provider warmup", flag.ExitOnError)
	wf := addWarmupFlags(fs)
	_ = fs.Parse(args[1:])
	f, err := provider.Load(c.home)
	if err != nil {
		return err
	}
	e, err := f.Find(name)
	if err != nil {
		return err
	}
	w := provider.WarmOptions{}
	if e.Warmup != nil {
		w = *e.Warmup
	}
	wf.apply(fs, &w.Times, &w.OnReset, &w.Model)
	if *wf.off {
		w = provider.WarmOptions{}
	} else if len(w.Times) == 0 && w.OnReset == "" {
		return errors.New("give -times or -on-reset, or -off")
	}
	if _, err := provider.SetWarmup(c.home, name, &w); err != nil {
		return err
	}
	return c.warmupStatus()
}

func (c *cmdCtx) warmupRun(args []string) error {
	var st warmup.Status
	if err := c.daemon(http.MethodGet, "/api/v1/warmup", nil, &st); err != nil {
		return err
	}
	var ids []string
	if len(args) > 0 {
		ids = args
	} else {
		for _, t := range st.Targets {
			if t.Enabled {
				ids = append(ids, t.ID)
			}
		}
		if len(ids) == 0 {
			return errors.New("no warm-up is set: name what to warm up (claude, codex, provider:<name>)")
		}
	}
	var failed error
	for _, id := range ids {
		var t warmup.TargetStatus
		if err := c.daemon(http.MethodPost, "/api/v1/warmup/run", map[string]string{"target": id}, &t); err != nil {
			fmt.Printf("%s: failed: %v\n", id, err)
			failed = errors.New("a warm-up failed")
			continue
		}
		fmt.Printf("%s: warmed up%s\n", targetLabel(t), modelNote(t.LastModel))
	}
	return failed
}

func modelNote(m string) string {
	if m == "" {
		return ""
	}
	return " (" + m + ")"
}

func targetLabel(t warmup.TargetStatus) string {
	if t.Kind == warmup.KindLocal {
		if t.Agent == warmup.AgentClaude {
			return "Claude Code (this machine)"
		}
		return "Codex (this machine)"
	}
	return "provider " + t.Name
}

func (c *cmdCtx) warmupStatus() error {
	var st warmup.Status
	running := true
	if err := c.daemon(http.MethodGet, "/api/v1/warmup", nil, &st); err != nil {
		running = false
		set, err := warmup.LoadSettings(c.home)
		if err != nil {
			return err
		}
		st = warmup.Status{Paused: set.Paused, Settings: set}
		for _, a := range []struct {
			agent string
			l     warmup.Local
		}{{warmup.AgentClaude, set.Claude}, {warmup.AgentCodex, set.Codex}} {
			st.Targets = append(st.Targets, warmup.TargetStatus{ID: "local:" + a.agent, Kind: warmup.KindLocal, Agent: a.agent,
				Enabled: a.l.Enabled, Times: a.l.Times, OnReset: a.l.OnReset, Model: a.l.Model})
		}
		if f, err := provider.Load(c.home); err == nil {
			for _, e := range f.Providers {
				if !e.WarmCapable() {
					continue
				}
				t := warmup.TargetStatus{ID: "provider:" + e.ID, Kind: warmup.KindProvider, Name: e.Name}
				if w := e.Warmup; w != nil {
					t.Enabled, t.Times, t.OnReset, t.Model = len(w.Times) > 0 || w.OnReset != "", w.Times, w.OnReset, w.Model
				}
				st.Targets = append(st.Targets, t)
			}
		}
	}
	if st.Paused {
		fmt.Println("warm-ups are paused (sharetoken warmup resume)")
	}
	if !running {
		fmt.Println("the client is not running: warm-ups are sent only while it runs (sharetoken up, or the desktop app)")
	} else if st.Zone != "" {
		fmt.Println("times of day are in", st.Zone)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ACCOUNT\tTIMES\tON RESET\tLAST\tWINDOWS\tNOTE")
	for _, t := range st.Targets {
		times, onReset := "off", "off"
		if t.Enabled {
			times = strings.Join(t.Times, ",")
			if times == "" {
				times = "—"
			}
			if t.OnReset != "" {
				onReset = t.OnReset
			}
		}
		last := "—"
		if !t.LastTry.IsZero() {
			last = t.Result + " " + t.LastTry.Local().Format("01-02 15:04")
		}
		note := t.Skip
		if t.Error != "" {
			note = t.Error
		} else if t.SkipError != "" {
			note = t.SkipError
		} else if t.Binary != "" && t.Kind == warmup.KindLocal {
			note = strings.TrimSpace(t.Binary + " " + t.Version)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", targetLabel(t), times, onReset, last, windowsText(t), note)
	}
	return tw.Flush()
}

func windowsText(t warmup.TargetStatus) string {
	if t.Usage == nil || len(t.Usage.Windows) == 0 {
		return "—"
	}
	var parts []string
	now := time.Now()
	for _, w := range t.Usage.Windows {
		p := fmt.Sprintf("%s %.0f%%", w.Name, w.Used)
		if !w.ResetsAt.IsZero() {
			if w.ResetsAt.After(now) {
				p += " → " + w.ResetsAt.Local().Format("15:04")
				if w.ResetsAt.Sub(now) > 24*time.Hour {
					p = fmt.Sprintf("%s %.0f%% → %s", w.Name, w.Used, w.ResetsAt.Local().Format("01-02 15:04"))
				}
			} else {
				p = w.Name + " reset"
			}
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}
