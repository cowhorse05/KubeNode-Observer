package topology

import "testing"

func TestRuntimePID(t *testing.T) {
	tests := []struct {
		name string
		info map[string]string
		want int
	}{
		{name: "containerd info", info: map[string]string{"info": `{"pid":3771,"runtimeType":"io.containerd.runc.v2"}`}, want: 3771},
		{name: "missing info", info: map[string]string{}, want: 0},
		{name: "invalid info", info: map[string]string{"info": "not-json"}, want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runtimePID(test.info); got != test.want {
				t.Fatalf("runtimePID() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRuntimeTypeAndKataClassification(t *testing.T) {
	if got := runtimeType(map[string]string{"info": `{"pid":42,"runtimeType":"io.containerd.kata.v2"}`}); got != "io.containerd.kata.v2" {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		runtime, handler string
		kata             bool
	}{
		{"io.containerd.kata.v2", "custom-handler", true},
		{"io.containerd.kata-qemu.v2", "custom-handler", true},
		{"", "kata-qemu", true},
		{"io.containerd.runc.v2", "kata-misleading-name", false},
		{"", "", false},
	} {
		if isKata(tc.runtime, tc.handler) != tc.kata {
			t.Fatal(tc)
		}
	}
}
