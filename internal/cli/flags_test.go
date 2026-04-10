package cli

import (
	"testing"
)

var testDefs = []FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name"},
	{Name: "--limit", Short: "-n", Arg: "N", Help: "Max results", Default: "10"},
	{Name: "--json", Help: "Output JSON"},
	{Name: "--verbose", Short: "-V", Help: "Verbose output"},
	{Name: "--dry-run", Help: "Dry run mode"},
}

func TestParseFlagsLong(t *testing.T) {
	fv, err := ParseFlags(testDefs, []string{"--project", "myapp", "--limit", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fv.Get("--project"); got != "myapp" {
		t.Errorf("--project = %q, want %q", got, "myapp")
	}
	if got := fv.Int("--limit"); got != 5 {
		t.Errorf("--limit = %d, want %d", got, 5)
	}
}

func TestParseFlagsShort(t *testing.T) {
	fv, err := ParseFlags(testDefs, []string{"-p", "myapp", "-n", "3"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fv.Get("--project"); got != "myapp" {
		t.Errorf("--project = %q, want %q", got, "myapp")
	}
	if got := fv.Int("--limit"); got != 3 {
		t.Errorf("--limit = %d, want %d", got, 3)
	}
}

func TestParseFlagsEquals(t *testing.T) {
	fv, err := ParseFlags(testDefs, []string{"--project=myapp", "--limit=7"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fv.Get("--project"); got != "myapp" {
		t.Errorf("--project = %q, want %q", got, "myapp")
	}
	if got := fv.Int("--limit"); got != 7 {
		t.Errorf("--limit = %d, want %d", got, 7)
	}
}

func TestParseFlagsBool(t *testing.T) {
	fv, err := ParseFlags(testDefs, []string{"--json", "--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if !fv.Bool("--json") {
		t.Error("--json should be true")
	}
	if !fv.Bool("--verbose") {
		t.Error("--verbose should be true")
	}
	if fv.Bool("--dry-run") {
		t.Error("--dry-run should be false")
	}
}

func TestParseFlagsBoolEquals(t *testing.T) {
	fv, err := ParseFlags(testDefs, []string{"--json=true", "--verbose=false"})
	if err != nil {
		t.Fatal(err)
	}
	if !fv.Bool("--json") {
		t.Error("--json=true should be true")
	}
	if fv.Bool("--verbose") {
		t.Error("--verbose=false should be false")
	}
}

func TestParseFlagsBoolInvalidValue(t *testing.T) {
	_, err := ParseFlags(testDefs, []string{"--json=maybe"})
	if err == nil {
		t.Error("expected error for invalid bool value")
	}
}

func TestParseFlagsPositional(t *testing.T) {
	fv, err := ParseFlags(testDefs, []string{"query", "--project", "myapp", "extra"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fv.Get("--project"); got != "myapp" {
		t.Errorf("--project = %q, want %q", got, "myapp")
	}
	args := fv.Args()
	if len(args) != 2 || args[0] != "query" || args[1] != "extra" {
		t.Errorf("Args() = %v, want [query extra]", args)
	}
}

func TestParseFlagsDoubleDash(t *testing.T) {
	fv, err := ParseFlags(testDefs, []string{"--project", "myapp", "--", "--json", "rest"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fv.Get("--project"); got != "myapp" {
		t.Errorf("--project = %q, want %q", got, "myapp")
	}
	if fv.Bool("--json") {
		t.Error("--json after -- should not be parsed as flag")
	}
	args := fv.Args()
	if len(args) != 2 || args[0] != "--json" || args[1] != "rest" {
		t.Errorf("Args() = %v, want [--json rest]", args)
	}
}

func TestParseFlagsUnknown(t *testing.T) {
	_, err := ParseFlags(testDefs, []string{"--unknown"})
	if err == nil {
		t.Error("expected error for unknown flag")
	}
}

func TestParseFlagsMissingValue(t *testing.T) {
	_, err := ParseFlags(testDefs, []string{"--project"})
	if err == nil {
		t.Error("expected error for missing value")
	}
}

func TestParseFlagsEmpty(t *testing.T) {
	fv, err := ParseFlags(testDefs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fv.Args()) != 0 {
		t.Errorf("Args() = %v, want empty", fv.Args())
	}
	if fv.Get("--project") != "" {
		t.Error("unset flag should return empty string")
	}
	if fv.Int("--limit") != 0 {
		t.Error("unset int flag should return 0")
	}
}

func TestParseFlagsMixed(t *testing.T) {
	fv, err := ParseFlags(testDefs, []string{
		"query", "-p", "app", "--json", "-n", "20", "extra",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fv.Get("--project"); got != "app" {
		t.Errorf("--project = %q", got)
	}
	if !fv.Bool("--json") {
		t.Error("--json should be true")
	}
	if got := fv.Int("--limit"); got != 20 {
		t.Errorf("--limit = %d", got)
	}
	args := fv.Args()
	if len(args) != 2 || args[0] != "query" || args[1] != "extra" {
		t.Errorf("Args() = %v", args)
	}
}
