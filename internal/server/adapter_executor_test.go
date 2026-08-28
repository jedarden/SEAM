package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestApplyRequestTransforms_Rename(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		transform AdapterTransform
		want     string
		wantErr  bool
	}{
		{
			name: "rename simple field",
			body: `{"oldName": "value"}`,
			transform: AdapterTransform{
				Rename: &RenameTransform{From: "/oldName", To: "/newName"},
			},
			want:    `{"newName":"value"}`,
			wantErr: false,
		},
		{
			name: "rename nested field",
			body: `{"data": {"oldField": "value"}}`,
			transform: AdapterTransform{
				Rename: &RenameTransform{From: "/data/oldField", To: "/data/newField"},
			},
			want:    `{"data":{"newField":"value"}}`,
			wantErr: false,
		},
		{
			name: "rename missing field - error",
			body: `{"other": "value"}`,
			transform: AdapterTransform{
				Rename: &RenameTransform{From: "/missing", To: "/new"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AdapterConfig{
				RequestTransforms: []AdapterTransform{tt.transform},
			}

			req, _ := http.NewRequest("POST", "/test", strings.NewReader(tt.body))
			got, err := ApplyRequestTransforms(req, []byte(tt.body), config)

			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyRequestTransforms() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Normalize JSON for comparison
				var gotJSON, wantJSON any
				json.Unmarshal(got, &gotJSON)
				json.Unmarshal([]byte(tt.want), &wantJSON)

				gotBytes, _ := json.Marshal(gotJSON)
				wantBytes, _ := json.Marshal(wantJSON)

				if string(gotBytes) != string(wantBytes) {
					t.Errorf("ApplyRequestTransforms() = %s, want %s", string(gotBytes), string(wantBytes))
				}
			}
		})
	}
}

func TestApplyRequestTransforms_Default(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		transform AdapterTransform
		want      string
		wantErr   bool
	}{
		{
			name: "default missing field",
			body: `{}`,
			transform: AdapterTransform{
				Default: &DefaultTransform{Pointer: "/newField", Value: "defaultValue"},
			},
			want:    `{"newField":"defaultValue"}`,
			wantErr: false,
		},
		{
			name: "default existing field - no change",
			body: `{"field": "existing"}`,
			transform: AdapterTransform{
				Default: &DefaultTransform{Pointer: "/field", Value: "default"},
			},
			want:    `{"field":"existing"}`,
			wantErr: false,
		},
		{
			name: "default nested field",
			body: `{"data": {}}`,
			transform: AdapterTransform{
				Default: &DefaultTransform{Pointer: "/data/count", Value: 0},
			},
			want:    `{"data":{"count":0}}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AdapterConfig{
				RequestTransforms: []AdapterTransform{tt.transform},
			}

			req, _ := http.NewRequest("POST", "/test", nil)
			got, err := ApplyRequestTransforms(req, []byte(tt.body), config)

			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyRequestTransforms() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				var gotJSON, wantJSON any
				json.Unmarshal(got, &gotJSON)
				json.Unmarshal([]byte(tt.want), &wantJSON)

				gotBytes, _ := json.Marshal(gotJSON)
				wantBytes, _ := json.Marshal(wantJSON)

				if string(gotBytes) != string(wantBytes) {
					t.Errorf("ApplyRequestTransforms() = %s, want %s", string(gotBytes), string(wantBytes))
				}
			}
		})
	}
}

func TestApplyRequestTransforms_Drop(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		transform AdapterTransform
		want      string
		wantErr   bool
	}{
		{
			name: "drop existing field",
			body: `{"keep": "value", "drop": "this"}`,
			transform: AdapterTransform{
				Drop: &DropTransform{Pointer: "/drop"},
			},
			want:    `{"keep":"value"}`,
			wantErr: false,
		},
		{
			name: "drop missing field - no-op",
			body: `{"keep": "value"}`,
			transform: AdapterTransform{
				Drop: &DropTransform{Pointer: "/missing"},
			},
			want:    `{"keep":"value"}`,
			wantErr: false,
		},
		{
			name: "drop nested field",
			body: `{"data": {"remove": "this", "keep": "that"}}`,
			transform: AdapterTransform{
				Drop: &DropTransform{Pointer: "/data/remove"},
			},
			want:    `{"data":{"keep":"that"}}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AdapterConfig{
				RequestTransforms: []AdapterTransform{tt.transform},
			}

			req, _ := http.NewRequest("POST", "/test", nil)
			got, err := ApplyRequestTransforms(req, []byte(tt.body), config)

			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyRequestTransforms() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				var gotJSON, wantJSON any
				json.Unmarshal(got, &gotJSON)
				json.Unmarshal([]byte(tt.want), &wantJSON)

				gotBytes, _ := json.Marshal(gotJSON)
				wantBytes, _ := json.Marshal(wantJSON)

				if string(gotBytes) != string(wantBytes) {
					t.Errorf("ApplyRequestTransforms() = %s, want %s", string(gotBytes), string(wantBytes))
				}
			}
		})
	}
}

func TestApplyRequestTransforms_Wrap(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		transform AdapterTransform
		want      string
		wantErr   bool
	}{
		{
			name: "wrap simple value",
			body: `{"data": "value"}`,
			transform: AdapterTransform{
				Wrap: &WrapTransform{Pointer: "/data", Envelope: "result"},
			},
			want:    `{"data":{"result":"value"}}`,
			wantErr: false,
		},
		{
			name: "wrap nested object",
			body: `{"user": {"name": "Alice"}}`,
			transform: AdapterTransform{
				Wrap: &WrapTransform{Pointer: "/user", Envelope: "profile"},
			},
			want:    `{"user":{"profile":{"name":"Alice"}}}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AdapterConfig{
				RequestTransforms: []AdapterTransform{tt.transform},
			}

			req, _ := http.NewRequest("POST", "/test", nil)
			got, err := ApplyRequestTransforms(req, []byte(tt.body), config)

			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyRequestTransforms() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				var gotJSON, wantJSON any
				json.Unmarshal(got, &gotJSON)
				json.Unmarshal([]byte(tt.want), &wantJSON)

				gotBytes, _ := json.Marshal(gotJSON)
				wantBytes, _ := json.Marshal(wantJSON)

				if string(gotBytes) != string(wantBytes) {
					t.Errorf("ApplyRequestTransforms() = %s, want %s", string(gotBytes), string(wantBytes))
				}
			}
		})
	}
}

func TestApplyRequestTransforms_Unwrap(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		transform AdapterTransform
		want      string
		wantErr   bool
	}{
		{
			name: "unwrap simple value",
			body: `{"data": {"result": "value"}}`,
			transform: AdapterTransform{
				Unwrap: &UnwrapTransform{Pointer: "/data", Envelope: "result"},
			},
			want:    `{"data":"value"}`,
			wantErr: false,
		},
		{
			name: "unwrap nested object",
			body: `{"user": {"profile": {"name": "Alice"}}}`,
			transform: AdapterTransform{
				Unwrap: &UnwrapTransform{Pointer: "/user", Envelope: "profile"},
			},
			want:    `{"user":{"name":"Alice"}}`,
			wantErr: false,
		},
		{
			name: "unwrap missing envelope - error",
			body: `{"data": {"other": "value"}}`,
			transform: AdapterTransform{
				Unwrap: &UnwrapTransform{Pointer: "/data", Envelope: "result"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AdapterConfig{
				RequestTransforms: []AdapterTransform{tt.transform},
			}

			req, _ := http.NewRequest("POST", "/test", nil)
			got, err := ApplyRequestTransforms(req, []byte(tt.body), config)

			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyRequestTransforms() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				var gotJSON, wantJSON any
				json.Unmarshal(got, &gotJSON)
				json.Unmarshal([]byte(tt.want), &wantJSON)

				gotBytes, _ := json.Marshal(gotJSON)
				wantBytes, _ := json.Marshal(wantJSON)

				if string(gotBytes) != string(wantBytes) {
					t.Errorf("ApplyRequestTransforms() = %s, want %s", string(gotBytes), string(wantBytes))
				}
			}
		})
	}
}

func TestApplyRequestTransforms_RenameParam(t *testing.T) {
	tests := []struct {
	name      string
	query     string
	transform AdapterTransform
	wantQuery string
	wantErr   bool
	}{
		{
			name:  "rename query parameter",
			query: "old=value",
			transform: AdapterTransform{
				RenameParam: &RenameParamTransform{From: "old", To: "new", Location: "query"},
			},
			wantQuery: "new=value",
			wantErr:   false,
		},
		{
			name:  "rename missing query param - no-op",
			query: "other=value",
			transform: AdapterTransform{
				RenameParam: &RenameParamTransform{From: "missing", To: "new", Location: "query"},
			},
			wantQuery: "other=value",
			wantErr:   false,
		},
		{
			name:  "rename multiple values",
			query: "old=1&old=2",
			transform: AdapterTransform{
				RenameParam: &RenameParamTransform{From: "old", To: "new", Location: "query"},
			},
			wantQuery: "new=1&new=2",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AdapterConfig{
				RequestTransforms: []AdapterTransform{tt.transform},
			}

			req, _ := http.NewRequest("GET", "/test?"+tt.query, nil)
			_, err := ApplyRequestTransforms(req, []byte("{}"), config)

			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyRequestTransforms() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Query parameters may be in different order, normalize
				gotQuery := req.URL.Query().Encode()
				gotValues := strings.Split(gotQuery, "&")
				wantValues := strings.Split(tt.wantQuery, "&")

				// Sort for comparison
				for i := 0; i < len(gotValues); i++ {
					for j := i + 1; j < len(gotValues); j++ {
						if gotValues[i] > gotValues[j] {
							gotValues[i], gotValues[j] = gotValues[j], gotValues[i]
						}
					}
				}
				for i := 0; i < len(wantValues); i++ {
					for j := i + 1; j < len(wantValues); j++ {
						if wantValues[i] > wantValues[j] {
							wantValues[i], wantValues[j] = wantValues[j], wantValues[i]
						}
					}
				}

				gotSorted := strings.Join(gotValues, "&")
				wantSorted := strings.Join(wantValues, "&")

				if gotSorted != wantSorted {
					t.Errorf("ApplyRequestTransforms() query = %s, want %s", gotSorted, wantSorted)
				}
			}
		})
	}
}

func TestRenameHeaderParam(t *testing.T) {
	tests := []struct {
		name   string
		header string
		from   string
		to     string
		want   string
	}{
		{
			name:   "rename header",
			header: "Old-Header: value",
			from:   "Old-Header",
			to:     "New-Header",
			want:   "New-Header: value",
		},
		{
			name:   "rename header case insensitive",
			header: "old-header: value",
			from:   "Old-Header",
			to:     "New-Header",
			want:   "New-Header: value",
		},
		{
			name:   "rename missing header - no-op",
			from:   "Missing",
			to:     "New",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "/test", nil)
			if tt.header != "" {
				parts := strings.SplitN(tt.header, ": ", 2)
				if len(parts) == 2 {
					req.Header.Add(parts[0], parts[1])
				}
			}

			renameHeaderParam(req, tt.from, tt.to)

			if tt.want == "" {
				if req.Header.Get(tt.to) != "" {
					t.Errorf("renameHeaderParam() = %v, want empty", req.Header.Get(tt.to))
				}
			} else {
				got := req.Header.Get(tt.to)
				wantParts := strings.SplitN(tt.want, ": ", 2)
				if got != wantParts[1] {
					t.Errorf("renameHeaderParam() = %v, want %v", got, wantParts[1])
				}
			}
		})
	}
}

func TestApplyResponseTransforms(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		transforms []AdapterTransform
		want       string
		wantErr    bool
	}{
		{
			name: "response rename",
			body: `{"oldField": "value"}`,
			transforms: []AdapterTransform{
				{Rename: &RenameTransform{From: "/oldField", To: "/newField"}},
			},
			want:    `{"newField":"value"}`,
			wantErr: false,
		},
		{
			name: "response drop",
			body: `{"keep": "a", "drop": "b"}`,
			transforms: []AdapterTransform{
				{Drop: &DropTransform{Pointer: "/drop"}},
			},
			want:    `{"keep":"a"}`,
			wantErr: false,
		},
		{
			name: "multiple transforms",
			body: `{"data": {"old": "value"}}`,
			transforms: []AdapterTransform{
				{Rename: &RenameTransform{From: "/data/old", To: "/data/new"}},
				{Default: &DefaultTransform{Pointer: "/data/added", Value: 42}},
			},
			want:    `{"data":{"new":"value","added":42}}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AdapterConfig{
				ResponseTransforms: tt.transforms,
			}

			got, err := ApplyResponseTransforms([]byte(tt.body), config)

			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyResponseTransforms() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				var gotJSON, wantJSON any
				json.Unmarshal(got, &gotJSON)
				json.Unmarshal([]byte(tt.want), &wantJSON)

				gotBytes, _ := json.Marshal(gotJSON)
				wantBytes, _ := json.Marshal(wantJSON)

				if string(gotBytes) != string(wantBytes) {
					t.Errorf("ApplyResponseTransforms() = %s, want %s", string(gotBytes), string(wantBytes))
				}
			}
		})
	}
}

func TestRequiresBufferedResponse(t *testing.T) {
	tests := []struct {
		name   string
		config *AdapterConfig
		want   bool
	}{
		{
			name:   "no adapter",
			config: nil,
			want:   false,
		},
		{
			name: "no response transforms",
			config: &AdapterConfig{
				ResponseTransforms: []AdapterTransform{},
			},
			want: false,
		},
		{
			name: "wrap requires buffering",
			config: &AdapterConfig{
				ResponseTransforms: []AdapterTransform{
					{Wrap: &WrapTransform{Pointer: "/data", Envelope: "result"}},
				},
			},
			want: true,
		},
		{
			name: "unwrap requires buffering",
			config: &AdapterConfig{
				ResponseTransforms: []AdapterTransform{
					{Unwrap: &UnwrapTransform{Pointer: "/data", Envelope: "result"}},
				},
			},
			want: true,
		},
		{
			name: "rename does not require buffering",
			config: &AdapterConfig{
				ResponseTransforms: []AdapterTransform{
					{Rename: &RenameTransform{From: "/old", To: "/new"}},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RequiresBufferedResponse(tt.config)
			if got != tt.want {
				t.Errorf("RequiresBufferedResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseAdapterTransforms(t *testing.T) {
	tests := []struct {
		name        string
		configInput map[string]any
		want        *AdapterConfig
		wantErr     bool
	}{
		{
			name: "valid rename transform",
			configInput: map[string]any{
				"targetVersion": "v2",
				"request": []any{
					map[string]any{
						"rename": map[string]any{
							"from": "/old",
							"to":   "/new",
						},
					},
				},
				"response": []any{},
			},
			want: &AdapterConfig{
				TargetVersion: "v2",
				RequestTransforms: []AdapterTransform{
					{Rename: &RenameTransform{From: "/old", To: "/new"}},
				},
				ResponseTransforms: []AdapterTransform{},
			},
			wantErr: false,
		},
		{
			name: "multiple transforms",
			configInput: map[string]any{
				"targetVersion": "v2",
				"request": []any{
					map[string]any{"drop": "/field1"},
					map[string]any{"rename": map[string]any{"from": "/old", "to": "/new"}},
				},
				"response": []any{
					map[string]any{"wrap": map[string]any{"pointer": "/data", "envelope": "result"}},
				},
			},
			want: &AdapterConfig{
				TargetVersion: "v2",
				RequestTransforms: []AdapterTransform{
					{Drop: &DropTransform{Pointer: "/field1"}},
					{Rename: &RenameTransform{From: "/old", To: "/new"}},
				},
				ResponseTransforms: []AdapterTransform{
					{Wrap: &WrapTransform{Pointer: "/data", Envelope: "result"}},
				},
			},
			wantErr: false,
		},
		{
			name: "missing targetVersion",
			configInput: map[string]any{
				"request": []any{},
				"response": []any{},
			},
			wantErr: true,
		},
		{
			name: "invalid transform type",
			configInput: map[string]any{
				"targetVersion": "v2",
				"request": []any{
					map[string]any{"unknown": "transform"},
				},
				"response": []any{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAdapterTransforms(tt.configInput)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseAdapterTransforms() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.TargetVersion != tt.want.TargetVersion {
					t.Errorf("ParseAdapterTransforms() TargetVersion = %v, want %v", got.TargetVersion, tt.want.TargetVersion)
				}
				// Compare transform counts
				if len(got.RequestTransforms) != len(tt.want.RequestTransforms) {
					t.Errorf("ParseAdapterTransforms() RequestTransforms count = %v, want %v", len(got.RequestTransforms), len(tt.want.RequestTransforms))
				}
				if len(got.ResponseTransforms) != len(tt.want.ResponseTransforms) {
					t.Errorf("ParseAdapterTransforms() ResponseTransforms count = %v, want %v", len(got.ResponseTransforms), len(tt.want.ResponseTransforms))
				}
			}
		})
	}
}

func TestApplyResponseTransformsToReader(t *testing.T) {
	config := &AdapterConfig{
		ResponseTransforms: []AdapterTransform{
			{Rename: &RenameTransform{From: "/old", To: "/new"}},
		},
	}

	input := `{"old": "value"}`
	reader := strings.NewReader(input)

	transformedReader, err := ApplyResponseTransformsToReader(reader, config)
	if err != nil {
		t.Fatalf("ApplyResponseTransformsToReader() error = %v", err)
	}

	got, err := io.ReadAll(transformedReader)
	if err != nil {
		t.Fatalf("failed to read transformed reader: %v", err)
	}

	var gotJSON, wantJSON any
	json.Unmarshal(got, &gotJSON)
	json.Unmarshal([]byte(`{"new":"value"}`), &wantJSON)

	gotBytes, _ := json.Marshal(gotJSON)
	wantBytes, _ := json.Marshal(wantJSON)

	if string(gotBytes) != string(wantBytes) {
		t.Errorf("ApplyResponseTransformsToReader() = %s, want %s", string(gotBytes), string(wantBytes))
	}
}

func TestTransformingReader(t *testing.T) {
	data := []byte(`{"test":"data"}`)
	reader := &transformingReader{data: data}

	// Read in chunks
	buf := make([]byte, 5)
	var result []byte

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			result = append(result, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("transformingReader.Read() error = %v", err)
		}
	}

	if string(result) != string(data) {
		t.Errorf("transformingReader = %s, want %s", string(result), string(data))
	}
}
