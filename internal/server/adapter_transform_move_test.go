package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestApplyRenameTransform_MoveSemantics covers the parts of rename that the
// adapter executor tests do not: a rename is a move, so the source key must
// survive only when the destination is the source itself.
func TestApplyRenameTransform_MoveSemantics(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		transform RenameTransform
		want      string
	}{
		{
			// Writing the destination then deleting the source would drop the
			// value entirely if both pointers name the same location
			name:      "rename onto itself keeps the value",
			body:      `{"same": "value"}`,
			transform: RenameTransform{From: "/same", To: "/same"},
			want:      `{"same":"value"}`,
		},
		{
			name:      "cross-parent move removes the source and keeps its parent",
			body:      `{"data": {"old": "value"}, "keep": true}`,
			transform: RenameTransform{From: "/data/old", To: "/moved"},
			want:      `{"data":{},"keep":true,"moved":"value"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AdapterConfig{
				RequestTransforms: []AdapterTransform{{Rename: &tt.transform}},
			}

			req, _ := http.NewRequest("POST", "/test", nil)
			got, err := ApplyRequestTransforms(req, []byte(tt.body), config)
			if err != nil {
				t.Fatalf("ApplyRequestTransforms() error = %v", err)
			}

			var gotJSON, wantJSON any
			if err := json.Unmarshal(got, &gotJSON); err != nil {
				t.Fatalf("failed to unmarshal result %s: %v", got, err)
			}
			if err := json.Unmarshal([]byte(tt.want), &wantJSON); err != nil {
				t.Fatalf("failed to unmarshal want %s: %v", tt.want, err)
			}

			gotBytes, _ := json.Marshal(gotJSON)
			wantBytes, _ := json.Marshal(wantJSON)

			if string(gotBytes) != string(wantBytes) {
				t.Errorf("ApplyRequestTransforms() = %s, want %s", gotBytes, wantBytes)
			}
		})
	}
}
