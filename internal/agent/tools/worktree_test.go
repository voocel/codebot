package tools

import "testing"

// "keep" must never be treated as discard, and an unknown or missing action
// is an error.
func TestExitWorktree_ActionMapsToDiscard(t *testing.T) {
	for _, tc := range []struct {
		args        string
		wantDiscard bool
		wantErr     bool
	}{
		{`{"action":"keep"}`, false, false},
		{`{"action":"discard"}`, true, false},
		{`{"action":"remove"}`, false, true},
		{`{}`, false, true},
	} {
		var gotDiscard bool
		tool := NewExitWorktree(func(discard bool) (string, error) {
			gotDiscard = discard
			return "left worktree", nil
		})
		if _, err := call(t, tool, tc.args); (err != nil) != tc.wantErr {
			t.Fatalf("%s: err = %v, wantErr %v", tc.args, err, tc.wantErr)
		}
		if gotDiscard != tc.wantDiscard {
			t.Errorf("%s → discard=%v, want %v", tc.args, gotDiscard, tc.wantDiscard)
		}
	}
}
