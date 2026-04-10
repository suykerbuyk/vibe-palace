package cli

import "testing"

func TestFlagDefIsBool(t *testing.T) {
	tests := []struct {
		name string
		def  FlagDef
		want bool
	}{
		{"bool flag", FlagDef{Name: "--verbose"}, true},
		{"value flag", FlagDef{Name: "--project", Arg: "PROJECT"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.def.IsBool(); got != tt.want {
				t.Errorf("IsBool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExitCodes(t *testing.T) {
	if ExitOK != 0 {
		t.Error("ExitOK should be 0")
	}
	if ExitUser != 1 {
		t.Error("ExitUser should be 1")
	}
	if ExitSystem != 2 {
		t.Error("ExitSystem should be 2")
	}
}
