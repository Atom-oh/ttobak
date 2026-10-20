package speaker

import "testing"

func TestReplacerUsesOriginalLabelsOnce(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		mapping          map[string]string
	}{
		{"swap", "Kim Lee", "Lee Kim", map[string]string{"Kim": "Lee", "Lee": "Kim"}},
		{"no recursive expansion", "spk_0 spk_1", "spk_1 spk_1 Kim", map[string]string{"spk_0": "spk_1 spk_1", "spk_1": "Kim"}},
		{"namespace", "spk_1 spk_1000000 spk_10", "Kim Lee spk_10", map[string]string{"spk_1": "Kim", "spk_1000000": "Lee"}},
		{"Korean particles", "spk_1이 화자A에게", "김팀장이 이대리에게", map[string]string{"spk_1": "김팀장", "화자A": "이대리"}},
		{"embedded text", "prefixspk_1 Kimball", "prefixspk_1 Kimball", map[string]string{"spk_1": "Kim", "Kim": "Lee"}},
		{"full name", "Kim Lee, Kim", "Park, Choi", map[string]string{"Kim": "Choi", "Kim Lee": "Park"}},
		{"literal regex characters", "Kim (TJ)", "Lee", map[string]string{"Kim (TJ)": "Lee"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewReplacer(tc.mapping).Replace(tc.text); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
