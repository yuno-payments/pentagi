package executor

import "testing"

func TestExecutor_PrimaryContainerPorts_IsDeterministicPerFlow(t *testing.T) {
	cases := []struct {
		name     string
		portsBase int
		flowID   int64
		want     []int
	}{
		{"flow 0 at default base", 28000, 0, []int{28000, 28001}},
		{"flow 5 offsets by flowID*2", 28000, 5, []int{28010, 28011}},
		{"zero base falls back to default", 0, 5, []int{28010, 28011}},
		{"out-of-range base falls back to default", 70000, 0, []int{28000, 28001}},
		{"offset wraps at the 2000 limit", 28000, 1000, []int{28000, 28001}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PrimaryContainerPorts(tc.portsBase, tc.flowID)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v want %v", got, tc.want)
				}
			}
		})
	}
}

func TestExecutor_WorkerCapabilities_GatesNetAdminAndNeverGrantsEscalation(t *testing.T) {
	forbidden := map[string]bool{"MKNOD": true, "SYS_ADMIN": true, "SYS_MODULE": true, "SYS_RAWIO": true, "SYS_BOOT": true}

	without := WorkerCapabilities(false)
	for _, c := range without {
		if forbidden[c] {
			t.Fatalf("forbidden capability %q present without netAdmin", c)
		}
		if c == "NET_ADMIN" {
			t.Fatalf("NET_ADMIN present when netAdmin=false")
		}
	}
	if !contains(without, "SYS_PTRACE") {
		t.Fatalf("SYS_PTRACE expected in the allow-list")
	}

	with := WorkerCapabilities(true)
	if !contains(with, "NET_ADMIN") {
		t.Fatalf("NET_ADMIN expected when netAdmin=true")
	}
	if len(with) != len(without)+1 {
		t.Fatalf("netAdmin should add exactly one capability: without=%d with=%d", len(without), len(with))
	}
	for _, c := range with {
		if forbidden[c] {
			t.Fatalf("forbidden capability %q present", c)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
