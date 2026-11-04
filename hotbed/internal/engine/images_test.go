package engine

import (
	"os/exec"
	"strings"
	"testing"
)

func TestImageSpecValidate(t *testing.T) {
	good := ImageSpec{Name: "tools", From: "python", Apt: []string{"jq", "libpq-dev", "ripgrep=14.1.1-1"}}
	if err := good.Validate(); err != nil {
		t.Fatalf("good spec: %v", err)
	}
	for _, bad := range []ImageSpec{
		{Name: "Tools", From: "base", Apt: []string{"jq"}},
		{Name: "t", From: "ubuntu", Apt: []string{"jq"}},
		{Name: "t", From: "base", Apt: []string{"jq; rm -rf /"}},
		{Name: "t", From: "base", Env: map[string]string{"A B": "x"}},
		{Name: "t", From: "custom:t", Apt: []string{"jq"}},
		{Name: "t", From: "base"},
	} {
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestImageSpecDockerfile(t *testing.T) {
	s := ImageSpec{Name: "t", From: "go", Apt: []string{"jq"}, Run: []string{`echo "a b" > /x`},
		Env: map[string]string{"B": "2", "A": `say "hi"`}}
	df := s.Dockerfile("hotbed:go", "go")
	for _, want := range []string{
		"FROM hotbed:go\nUSER root\n",
		`ENV A="say \"hi\""` + "\nENV B=\"2\"\n", // sorted, JSON-quoted
		`RUN ["/bin/bash","-euo","pipefail","-c","echo \"a b\" \u003e /x"]`,
		`dev.hotbed.base="go"`,
	} {
		if !strings.Contains(df, want) {
			t.Fatalf("dockerfile lacks %q:\n%s", want, df)
		}
	}
}

func TestCustomStack(t *testing.T) {
	if n, ok := CustomStack("custom:x"); !ok || n != "x" {
		t.Fatal("custom:x")
	}
	if _, ok := CustomStack("custom:"); ok {
		t.Fatal("empty custom name accepted")
	}
	if RecipeImageName("custom:x") != "hotbed-custom:x" || RecipeImageName("go") != "hotbed:go" {
		t.Fatal("image names")
	}
}

// The scrub greps with SecretPattern (POSIX ERE): real-looking values
// match, code that merely names the fields does not.
func TestSecretPattern(t *testing.T) {
	if _, err := exec.LookPath("grep"); err != nil {
		t.Skip("no grep")
	}
	match := func(s string) bool {
		c := exec.Command("grep", "-qE", SecretPattern)
		c.Stdin = strings.NewReader(s + "\n")
		return c.Run() == nil
	}
	for _, leak := range []string{
		"export OPENAI_API_KEY=hbk-codex-openwebui-0123456789abcdef0123",
		"key: sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		`{"refresh_token": "abcdefghijklmnopqrstuvwxyz0123"}`,
		"ghp_" + strings.Repeat("a", 36),
	} {
		if !match(leak) {
			t.Fatalf("missed %q", leak)
		}
	}
	for _, fine := range []string{
		`const refresh_token = body.refresh_token;`,
		`params["access_token"] = token`,
		"hbk-<agent>-<provider>-...",
		"see sk-ant docs",
	} {
		if match(fine) {
			t.Fatalf("false positive %q", fine)
		}
	}
}
