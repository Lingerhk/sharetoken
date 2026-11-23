package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"sharetoken/internal/client"
	"sharetoken/internal/official"
	"sharetoken/internal/provider"
)

// running reports whether a client answers at the address run.json names.
func (c *cmdCtx) running() bool {
	if _, key := client.LocalAPI(c.home, c.cfg); key == "" {
		return false
	}
	var h struct{}
	return c.daemon(http.MethodGet, "/api/v1/official", nil, &h) == nil
}

// connect points Claude Code or Codex at the local endpoint (their own
// config files; backups in the home's backups directory).
//
//	connect                                   the tools' connections
//	connect claude [-subscription auto|on|off] [-force] [-off]
//	connect codex  [-subscription auto|on|off] [-openai-provider] [-force] [-off]
func (c *cmdCtx) connect(args []string) error {
	if len(args) == 0 {
		v, err := client.ConnectStatus(c.home)
		if err != nil {
			return err
		}
		show := func(name, path string, signedIn bool, a any, sub bool, at time.Time) {
			state := "not connected"
			if a != nil {
				mode := "ShareToken only (API key)"
				if sub {
					mode = "with its own login, ShareToken models as st-…"
				}
				state = "connected " + at.Format(time.DateTime) + ", " + mode
			}
			fmt.Printf("%-12s %s\n             %s (signed in: %t)\n", name, state, path, signedIn)
		}
		if a := v.State.Claude; a != nil {
			show("Claude Code", v.ClaudePath, v.ClaudeSignedIn, a, a.Options.Subscription, a.At)
		} else {
			show("Claude Code", v.ClaudePath, v.ClaudeSignedIn, nil, false, time.Time{})
		}
		if a := v.State.Codex; a != nil {
			show("Codex", v.CodexPath, v.CodexChatGPT, a, a.Options.Subscription, a.At)
		} else {
			show("Codex", v.CodexPath, v.CodexChatGPT, nil, false, time.Time{})
		}
		return nil
	}
	tool := args[0]
	if tool != "claude" && tool != "codex" {
		return fmt.Errorf("connect claude | codex")
	}
	fs := flag.NewFlagSet("connect "+tool, flag.ExitOnError)
	sub := fs.String("subscription", "auto", "keep the tool's own login (claude.ai / ChatGPT) next to ShareToken: auto (when signed in), on, off")
	openai := fs.Bool("openai-provider", false, "codex: use Codex's built-in OpenAI provider (earlier conversations stay listed)")
	force := fs.Bool("force", false, "replace a base URL that points at another gateway")
	off := fs.Bool("off", false, "put the tool's settings back as they were")
	_ = fs.Parse(args[1:])
	req := client.ConnectRequest{Tool: tool, Off: *off, OpenAIProvider: *openai, Force: *force}
	switch *sub {
	case "on", "true":
		t := true
		req.Subscription = &t
	case "off", "false":
		f := false
		req.Subscription = &f
	case "auto", "":
	default:
		return fmt.Errorf("-subscription is auto, on or off")
	}
	if c.running() {
		if err := c.daemon(http.MethodPost, "/api/v1/connect", req, nil); err != nil {
			return err
		}
	} else {
		addr, _ := client.LocalAPI(c.home, c.cfg)
		if err := client.ApplyConnect(c.home, "http://"+addr, nil, req); err != nil {
			return err
		}
		if !*off && tool == "claude" {
			fmt.Println("note: the client is not running; its models are added to Claude Code's menu once it runs")
		}
	}
	if *off {
		fmt.Printf("%s: settings put back (backups in %s)\n", tool, c.home+string(os.PathSeparator)+"backups")
		return nil
	}
	v, _ := client.ConnectStatus(c.home)
	if on, _ := client.AutostartStatus(); !on && !client.AppAutostartOn() {
		fmt.Println("warning: the client does not start at login. After a restart, a logout, or quitting the client")
		fmt.Println("(or the desktop app), the connected tool cannot reach this endpoint, not even for its own official")
		fmt.Println("models, until the client runs again. `sharetoken autostart on` (or the desktop app's setting under")
		fmt.Println("Settings > Tools) starts it at login; otherwise run `sharetoken connect " + tool + " -off` before you stop it.")
	}
	if tool == "claude" {
		fmt.Printf("Claude Code connected: %s (backup in %s)\n", v.ClaudePath, c.home+string(os.PathSeparator)+"backups")
		if a := v.State.Claude; a != nil && a.Options.Subscription {
			fmt.Println("It keeps its claude.ai login: /model lists ShareToken's models (st-…) after the official ones.")
			fmt.Println("When the plan is used up, requests go on on ShareToken (`sharetoken official fallback off` stops that).")
		}
		fmt.Println("Restart Claude Code to pick it up.")
	} else {
		fmt.Printf("Codex connected: %s (backup in %s)\n", v.CodexPath, c.home+string(os.PathSeparator)+"backups")
		if a := v.State.Codex; a != nil && a.Options.Subscription {
			fmt.Println("It keeps its ChatGPT login: the model menu lists ShareToken's models (st-…) after the official ones.")
			fmt.Println("Codex Desktop: `sharetoken codex-unlock on` keeps a spent plan from locking the input.")
		}
		fmt.Println("Restart Codex (and Codex Desktop) to pick it up.")
	}
	return nil
}

// codexUnlock turns the Codex Desktop unlock on or off.
func (c *cmdCtx) codexUnlock(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		s, _ := official.LoadSettings(c.home)
		fmt.Printf("codex-unlock %s (certificate trusted: %t)\nusage: codex-unlock on | off\n", onOff(s.CodexUnlock), official.UnlockCertTrusted(c.home))
		return nil
	}
	on := args[0] == "on"
	if on {
		fmt.Println("Codex Desktop's ChatGPT requests will go through this client, which keeps a spent plan from")
		fmt.Println("locking its input (official models still stop; ShareToken models go on). The system asks you to")
		fmt.Println("trust a certificate valid for localhost only, and port", official.UnlockPort(), "is used on this machine (loopback).")
		fmt.Println("While it is on, Codex Desktop needs the client running. At your own risk: it relies on how")
		fmt.Println("Codex Desktop works today.")
	}
	addr, _ := client.LocalAPI(c.home, c.cfg)
	if err := client.SetCodexUnlock(c.home, "http://"+addr, on, nil); err != nil {
		return err
	}
	if c.running() {
		s, _ := official.LoadSettings(c.home)
		if err := c.daemon(http.MethodPut, "/api/v1/official", s, nil); err != nil {
			return err
		}
	}
	fmt.Printf("codex-unlock %s: quit Codex Desktop (Cmd-Q) and open it again\n", onOff(on))
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// officialCmd shows the vendor logins' usage, and sets what happens when a
// plan runs out.
//
//	official                         usage and settings
//	official fallback on|off         Claude Code: go on on ShareToken when the plan is used up
//	official fallback-model <m|auto> the ShareToken model that takes over
//	official guard <percent>         Codex: hold official models at this share of a window (100: off)
func (c *cmdCtx) officialCmd(args []string) error {
	var st official.Status
	live := c.running()
	if live {
		if err := c.daemon(http.MethodGet, "/api/v1/official", nil, &st); err != nil {
			return err
		}
	} else {
		s, err := official.LoadSettings(c.home)
		if err != nil {
			return err
		}
		st.Settings = s
	}
	s := st.Settings
	if len(args) > 0 {
		switch {
		case args[0] == "fallback" && len(args) == 2 && (args[1] == "on" || args[1] == "off"):
			s.ClaudeFallbackOff = args[1] == "off"
		case args[0] == "fallback-model" && len(args) == 2:
			s.ClaudeFallbackModel = strings.TrimPrefix(args[1], "st-")
			if args[1] == "auto" {
				s.ClaudeFallbackModel = ""
			}
		case args[0] == "guard" && len(args) == 2:
			v, err := strconv.ParseFloat(strings.TrimSuffix(args[1], "%"), 64)
			if err != nil || v <= 0 || v > 100 {
				return fmt.Errorf("guard is a percentage, 1 to 100")
			}
			s.CodexGuardPercent = v
		default:
			return fmt.Errorf("official [fallback on|off | fallback-model <model|auto> | guard <percent>]")
		}
		if live {
			if err := c.daemon(http.MethodPut, "/api/v1/official", s, &st); err != nil {
				return err
			}
		} else if err := official.SaveSettings(c.home, s); err != nil {
			return err
		}
	}
	model := s.ClaudeFallbackModel
	if model == "" {
		model = "auto (like the model asked for)"
	}
	guard := s.CodexGuardPercent
	if guard == 0 {
		guard = official.DefaultCodexGuard
	}
	fmt.Printf("Claude Code fallback to ShareToken  %s, model %s\n", onOff(!s.ClaudeFallbackOff), model)
	if s.CodexUnlock {
		fmt.Println("Codex guard                         off (codex-unlock is on)")
	} else {
		fmt.Printf("Codex guard                         holds official models at %.0f%%\n", guard)
	}
	if !live {
		fmt.Println("(the client is not running: no usage seen)")
		return nil
	}
	if u := st.Claude.Usage; u != nil {
		fmt.Printf("Claude plan                         %s\n", windows(u.Windows))
	}
	if !st.Claude.HeldUntil.IsZero() {
		fmt.Printf("                                    used up until %s: requests go to ShareToken\n", st.Claude.HeldUntil.Local().Format(time.DateTime))
	}
	if st.Claude.Fallbacks > 0 {
		fmt.Printf("                                    %d requests answered by ShareToken, last %s on %s\n", st.Claude.Fallbacks, st.Claude.LastFallback.Local().Format(time.DateTime), st.Claude.LastModel)
	}
	for _, x := range st.Codex {
		fmt.Printf("ChatGPT plan %s                %s (held %d)\n", x.Account, windows(x.Usage.Windows), x.Held)
	}
	if s.CodexUnlock {
		fmt.Printf("Codex unlock                        listening %t on port %d, certificate trusted %t %s\n", st.Unlock.Listening, st.Unlock.Port, st.Unlock.Trusted, st.Unlock.Error)
	}
	return nil
}

func windows(ws []provider.UsageWindow) string {
	var parts []string
	for _, w := range ws {
		p := fmt.Sprintf("%s %.0f%%", w.Name, w.Used)
		if !w.ResetsAt.IsZero() {
			p += " (resets " + w.ResetsAt.Local().Format("01-02 15:04") + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

// autostart runs the client's daemon at login.
func (c *cmdCtx) autostart(args []string) error {
	if len(args) == 1 && (args[0] == "on" || args[0] == "off") {
		if err := client.SetAutostart(c.home, args[0] == "on"); err != nil {
			return err
		}
	} else if len(args) > 0 {
		return fmt.Errorf("autostart [on | off]")
	}
	on, where := client.AutostartStatus()
	fmt.Printf("autostart %s (%s)\n", onOff(on), where)
	return nil
}
