package tools

import "testing"

func TestPolicyFor(t *testing.T) {
	cases := []struct {
		name     string
		wantOK   bool
		wantCat  FileCategory
		wantRead ReadScope
	}{
		{"SOUL.md", true, CategoryIdentity, ScopeOwner},
		{"IDENTITY.md", true, CategoryIdentity, ScopeOwner},
		{"agent.json", true, CategoryIdentity, ScopeOwner},
		{"AGENTS.md", true, CategoryScaffold, ScopeOwner},
		{"BOOTSTRAP.md", true, CategoryScaffold, ScopeOwner},
		{"HEARTBEAT.md", true, CategoryScaffold, ScopeOwner},
		{"TOOLS.md", true, CategoryScaffold, ScopeOwner},
		{"USER.md", true, CategoryPerUser, ScopeChatter},
		{"MEMORY.md", true, CategoryPerUser, ScopeChatter},
		{"report.md", false, "", ""},
		{"", false, "", ""},
	}
	for _, c := range cases {
		p, ok := PolicyFor(c.name)
		if ok != c.wantOK {
			t.Errorf("PolicyFor(%q) ok=%v want %v", c.name, ok, c.wantOK)
			continue
		}
		if ok && (p.Category != c.wantCat || p.ReadScope != c.wantRead) {
			t.Errorf("PolicyFor(%q) = {Cat:%s,Read:%s}, want {Cat:%s,Read:%s}",
				c.name, p.Category, p.ReadScope, c.wantCat, c.wantRead)
		}
	}
}

func TestManagedFileBasePathShapes(t *testing.T) {
	cases := []struct {
		path     string
		wantBase string
		wantMgd  bool
	}{
		{"SOUL.md", "SOUL.md", true},                            // bare
		{"/var/lib/lununda/agents/xyz/SOUL.md", "SOUL.md", true}, // absolute
		{"./SOUL.md", "SOUL.md", true},                           // dot-relative bare
		{"notes/SOUL.md", "", false},                             // nested → not managed
		{"report.md", "", false},                                 // unknown name
		{"", "", false},
	}
	for _, c := range cases {
		base, mgd := ManagedFileBase(c.path)
		if base != c.wantBase || mgd != c.wantMgd {
			t.Errorf("ManagedFileBase(%q) = (%q,%v), want (%q,%v)",
				c.path, base, mgd, c.wantBase, c.wantMgd)
		}
	}
}

func TestWriteAllowed(t *testing.T) {
	cases := []struct {
		path  string
		actor WriteActor
		want  bool
	}{
		{"SOUL.md", ActorOwner, true},
		{"SOUL.md", ActorChatter, false},
		{"agent.json", ActorChatter, false},
		{"AGENTS.md", ActorChatter, false},
		{"USER.md", ActorChatter, true},
		{"USER.md", ActorOwner, true},
		{"MEMORY.md", ActorOwner, true},
		{"/var/x/SOUL.md", ActorChatter, false},  // absolute still managed
		{"notes/SOUL.md", ActorChatter, true},    // nested → not managed → allowed
		{"report.md", ActorChatter, true},        // unknown → allowed
	}
	for _, c := range cases {
		if got := WriteAllowed(c.path, c.actor); got != c.want {
			t.Errorf("WriteAllowed(%q, %s) = %v, want %v", c.path, c.actor, got, c.want)
		}
	}
}

func TestIsChatterScoped(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"USER.md", true},
		{"MEMORY.md", true},
		{"SOUL.md", false},
		{"agent.json", false},
		{"report.md", false},
	}
	for _, c := range cases {
		if got := IsChatterScoped(c.name); got != c.want {
			t.Errorf("IsChatterScoped(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
