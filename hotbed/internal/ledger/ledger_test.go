package ledger

import "testing"

// TestAgentSplit: the summary breaks a sandbox down per agent ("" = shared
// key), heaviest first, with model counts.
func TestAgentSplit(t *testing.T) {
	t.Setenv("HOTBEDD_STATE_DIR", t.TempDir())
	l := Open(nil)
	for _, c := range []Call{
		{Sandbox: "s", Agent: "codex", Model: "m1", Status: 200, In: 100, Out: 10},
		{Sandbox: "s", Agent: "codex", Model: "m2", Status: 200, In: 50, Out: 5},
		{Sandbox: "s", Agent: "hermes", Model: "m1", Status: 200, In: 20, Out: 2},
		{Sandbox: "s", Model: "m1", Status: 200, In: 1, Out: 1},
		{Sandbox: "other", Agent: "codex", Model: "m1", Status: 200, In: 999, Out: 9},
	} {
		if _, err := l.Record(c); err != nil {
			t.Fatal(err)
		}
	}
	s := l.Summarize("s")
	if len(s.Agents) != 3 {
		t.Fatalf("agents = %+v", s.Agents)
	}
	a := s.Agents[0]
	if a.Agent != "codex" || a.Calls != 2 || a.In != 150 || a.Out != 15 || a.Models != 2 || a.Today != 2 {
		t.Errorf("codex row = %+v", a)
	}
	if s.Agents[1].Agent != "hermes" || s.Agents[2].Agent != "" {
		t.Errorf("order = %s, %s", s.Agents[1].Agent, s.Agents[2].Agent)
	}
	if s.Calls25[0].Agent != "" || s.Calls25[1].Agent != "hermes" {
		t.Errorf("recent rows lost their agent: %+v", s.Calls25[:2])
	}
}

// TestFailedCallsBookNoEstimates: rejected upstream calls keep their row
// but cost nothing; reported usage and successful estimates stay as-is.
func TestFailedCallsBookNoEstimates(t *testing.T) {
	t.Setenv("HOTBEDD_STATE_DIR", t.TempDir())
	l := Open(map[string][2]float64{"m": {1, 1}})
	cases := []struct {
		name    string
		c       Call
		in, out int
	}{
		{"400 estimated", Call{Sandbox: "s", Model: "m", Status: 400, In: 18000, Est: true}, 0, 0},
		{"200 estimated", Call{Sandbox: "s", Model: "m", Status: 200, In: 500, Out: 20, Est: true}, 500, 20},
		{"500 reported", Call{Sandbox: "s", Model: "m", Status: 500, In: 30, Out: 2}, 30, 2},
	}
	for _, c := range cases {
		e, err := l.Record(c.c)
		if err != nil {
			t.Fatal(err)
		}
		if e.In != c.in || e.Out != c.out {
			t.Errorf("%s: booked %d/%d, want %d/%d", c.name, e.In, e.Out, c.in, c.out)
		}
		if c.in == 0 && (e.Cost != 0 || e.Est) {
			t.Errorf("%s: cost %v est %v, want 0/false", c.name, e.Cost, e.Est)
		}
	}
}
