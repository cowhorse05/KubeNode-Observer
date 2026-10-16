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
