package agentskill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAgents(t *testing.T) (home string, agents []Agent) {
	home = t.TempDir()
	env := map[string]string{}
	return home, Agents(func(k string) string { return env[k] }, home)
}

func TestRender(t *testing.T) {
	s := Render(Params{Command: "sharetoken", Env: "prod", Portal: "https://sharetoken.app", Home: "/h/.sharetoken", HTTP: "127.0.0.1:9787"})
	if !strings.HasPrefix(s, "---\nname: sharetoken\ndescription: ") {
		t.Fatalf("front matter: %q", s[:60])
	}
	if strings.Contains(s, "PowerShell") || !strings.Contains(Render(Params{OS: "windows"}), "PowerShell") {
		t.Fatal("the Windows note belongs only in a Windows copy")
	}
	for _, p := range []string{"@CMD@", "@ENV@", "@PORTAL@", "@HOME@", "@HTTP@", "@PLATFORM@"} {
		if strings.Contains(s, p) {
			t.Fatalf("placeholder %s left", p)
		}
	}
	if !strings.Contains(s, "http://127.0.0.1:9787/v1") || !ours(s) {
		t.Fatal("endpoint or marker missing")
	}
	// Agent Skills: the description is one line and at most 1024 characters.
	desc := strings.SplitN(strings.SplitN(s, "description: ", 2)[1], "\n", 2)[0]
	if len(desc) > 1024 {
		t.Fatalf("description is %d characters", len(desc))
	}
}

func TestAgentsDirs(t *testing.T) {
	env := map[string]string{"CODEX_HOME": "/x/codex"}
	as := Agents(func(k string) string { return env[k] }, "/u")
	if as[0].Path() != filepath.Join("/u", ".claude", "skills", "sharetoken", "SKILL.md") {
		t.Fatal(as[0].Path())
	}
	if as[1].Path() != filepath.Join("/x/codex", "skills", "sharetoken", "SKILL.md") {
		t.Fatal(as[1].Path())
	}
}

func TestSelect(t *testing.T) {
	home, as := testAgents(t)
	if _, err := Select(as, ""); err == nil {
		t.Fatal("no agent present: want an error")
	}
	os.Mkdir(filepath.Join(home, ".codex"), 0o755)
	sel, err := Select(as, "")
	if err != nil || len(sel) != 1 || sel[0].ID != "codex" {
		t.Fatal(sel, err)
	}
	if sel, _ := Select(as, "all"); len(sel) != 2 {
		t.Fatal(sel)
	}
	if sel, _ := Select(as, "claude, claude"); len(sel) != 1 || sel[0].ID != "claude" {
		t.Fatal(sel)
	}
	if _, err := Select(as, "cursor"); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

func TestInstallRemove(t *testing.T) {
	_, as := testAgents(t)
	a := as[0]
	v1 := Render(Params{HTTP: "127.0.0.1:1"})
	v2 := Render(Params{HTTP: "127.0.0.1:2"})

	if r, err := Install(a, v1, false); err != nil || !r.Changed {
		t.Fatal(r, err)
	}
	if r, err := Install(a, v1, false); err != nil || r.Changed {
		t.Fatal("second install should change nothing", r, err)
	}
	if p, cur := Installed(a, v2); !p || cur {
		t.Fatal(p, cur)
	}
	if r, err := Install(a, v2, false); err != nil || !r.Changed { // an update replaces our own copy
		t.Fatal(r, err)
	}
	if r, err := Remove(a, false); err != nil || !r.Changed {
		t.Fatal(r, err)
	}
	if _, err := os.Stat(filepath.Dir(a.Path())); !os.IsNotExist(err) {
		t.Fatal("skill directory left behind")
	}
	if r, err := Remove(a, false); err != nil || r.Changed {
		t.Fatal(r, err)
	}
}

func TestForeignSkillKept(t *testing.T) {
	_, as := testAgents(t)
	a := as[1]
	os.MkdirAll(filepath.Dir(a.Path()), 0o755)
	foreign := "---\nname: sharetoken\ndescription: mine\n---\n"
	os.WriteFile(a.Path(), []byte(foreign), 0o644)
	content := Render(Params{})
	if _, err := Install(a, content, false); err == nil {
		t.Fatal("replaced a foreign skill without -force")
	}
	if _, err := Remove(a, false); err == nil {
		t.Fatal("removed a foreign skill without -force")
	}
	if b, _ := os.ReadFile(a.Path()); string(b) != foreign {
		t.Fatal("foreign skill changed")
	}
	if r, err := Install(a, content, true); err != nil || !r.Changed {
		t.Fatal(r, err)
	}
}

func TestRemoveKeepsUserFiles(t *testing.T) {
	_, as := testAgents(t)
	a := as[0]
	Install(a, Render(Params{}), false)
	extra := filepath.Join(filepath.Dir(a.Path()), "notes.md")
	os.WriteFile(extra, []byte("x"), 0o644)
	if _, err := Remove(a, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatal("a file the user added was removed")
	}
}
