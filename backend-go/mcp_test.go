package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mcpCall sends one JSON-RPC request through the dispatcher and decodes the
// reply, failing the test on a protocol error.
func mcpCall(t *testing.T, method string, params interface{}) json.RawMessage {
	t.Helper()
	p, _ := json.Marshal(params)
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":%q,"params":%s}`, method, p)
	reply := handleMCPMessage(json.RawMessage(req))
	if reply == nil {
		t.Fatalf("%s: no reply", method)
	}
	if reply.Error != nil {
		t.Fatalf("%s: error %d %s", method, reply.Error.Code, reply.Error.Message)
	}
	data, _ := json.Marshal(reply.Result)
	return data
}

// callTool runs a tool and returns its result, its first text block decoded
// into out when out is non-nil.
func callTool(t *testing.T, name string, args map[string]interface{}, out interface{}) mcpToolResult {
	t.Helper()
	var result mcpToolResult
	if err := json.Unmarshal(mcpCall(t, "tools/call", map[string]interface{}{"name": name, "arguments": args}), &result); err != nil {
		t.Fatal(err)
	}
	if out != nil && !result.IsError {
		if err := json.Unmarshal([]byte(result.Content[0].Text), out); err != nil {
			t.Fatalf("%s: decoding %q: %v", name, result.Content[0].Text, err)
		}
	}
	return result
}

func TestMCPInitializeNegotiatesVersion(t *testing.T) {
	var res struct {
		ProtocolVersion string                 `json:"protocolVersion"`
		Capabilities    map[string]interface{} `json:"capabilities"`
		Instructions    string                 `json:"instructions"`
	}
	json.Unmarshal(mcpCall(t, "initialize", map[string]interface{}{"protocolVersion": "2025-06-18"}), &res)
	if res.ProtocolVersion != "2025-06-18" {
		t.Errorf("a supported version should be echoed, got %q", res.ProtocolVersion)
	}
	if _, ok := res.Capabilities["tools"]; !ok || res.Instructions == "" {
		t.Errorf("expected tools capability and instructions, got %+v", res)
	}

	json.Unmarshal(mcpCall(t, "initialize", map[string]interface{}{"protocolVersion": "1999-01-01"}), &res)
	if res.ProtocolVersion != mcpLatestProtocol {
		t.Errorf("an unknown version should get the latest, got %q", res.ProtocolVersion)
	}
}

func TestMCPNotificationsGetNoReply(t *testing.T) {
	if r := handleMCPPayload([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); r != nil {
		t.Errorf("notification answered: %+v", r)
	}
	r := handleMCPPayload([]byte(`{"jsonrpc":"2.0","id":1,"method":"nope"}`)).(*mcpResponse)
	if r.Error == nil || r.Error.Code != rpcMethodNotFound {
		t.Errorf("expected method not found, got %+v", r)
	}
}

func TestMCPToolsListMatchesTools(t *testing.T) {
	var res struct {
		Tools []struct {
			Name        string                 `json:"name"`
			InputSchema map[string]interface{} `json:"inputSchema"`
		} `json:"tools"`
	}
	json.Unmarshal(mcpCall(t, "tools/list", map[string]interface{}{}), &res)
	names := []string{}
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.InputSchema["type"] != "object" {
			t.Errorf("%s: input schema must be an object", tool.Name)
		}
	}
	want := "list_sessions,list_photos,analyze_photo,preview_edit,apply_edit,revert_edit"
	if strings.Join(names, ",") != want {
		t.Errorf("tools = %v", names)
	}
}

func TestMCPListSessionsAndPhotos(t *testing.T) {
	session := withTestSession(t, "shot.png", skySceneGrey(t, 250, 70, 96))

	var sessions []sessionDirectory
	callTool(t, "list_sessions", map[string]interface{}{}, &sessions)
	if len(sessions) != 1 || sessions[0].Name != session || sessions[0].PhotoCount != 1 {
		t.Fatalf("sessions = %+v", sessions)
	}

	var photos struct {
		Photos []mcpPhotoEntry `json:"photos"`
	}
	callTool(t, "list_photos", map[string]interface{}{"session": session}, &photos)
	if len(photos.Photos) != 1 || photos.Photos[0].Name != "shot.png" || !photos.Photos[0].Editable || photos.Photos[0].Edit != nil {
		t.Fatalf("photos = %+v", photos)
	}
}

// TestMCPPreviewRecoversTheSky walks the workflow the server's instructions
// describe: a clipped sky, a preview that brings it under white without
// touching the disk, then the edit saved for real.
func TestMCPPreviewRecoversTheSky(t *testing.T) {
	// A sky that the exposure lift pushes past white is what highlight recovery
	// can bring back, so the "before" sky reads clipped after +1 stop.
	session := withTestSession(t, "shot.png", skySceneGrey(t, 200, 70, 96))
	photoPath := filepath.Join(photoBaseDir, session, "shot.png")
	pristine, _ := os.ReadFile(photoPath)

	var blown struct {
		After frameStats `json:"after"`
	}
	callTool(t, "preview_edit", map[string]interface{}{
		"session": session, "photo": "shot.png", "exposure": 1, "include_image": false,
	}, &blown)
	if blown.After.Regions["top"].ClippedPct < 50 {
		t.Fatalf("expected a blown sky after +1 stop, got %+v", blown.After.Regions["top"])
	}

	var fixed struct {
		After frameStats `json:"after"`
	}
	res := callTool(t, "preview_edit", map[string]interface{}{
		"session": session, "photo": "shot.png", "exposure": 1, "highlights": -80, "sky": 60,
	}, &fixed)
	if got := fixed.After.Regions["top"].ClippedPct; got != 0 {
		t.Errorf("sky still clipped after recovery: %v%%", got)
	}
	if len(res.Content) != 2 || res.Content[1].Type != "image" || res.Content[1].MimeType != "image/jpeg" {
		t.Fatalf("expected a JPEG preview, got %+v", res.Content)
	}
	img, err := base64.StdEncoding.DecodeString(res.Content[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(img)); err != nil {
		t.Fatalf("preview is not a JPEG: %v", err)
	}
	if now, _ := os.ReadFile(photoPath); !bytes.Equal(now, pristine) {
		t.Fatal("preview_edit must not touch the photo")
	}

	var applied struct {
		Status string    `json:"status"`
		Edit   photoEdit `json:"edit"`
	}
	callTool(t, "apply_edit", map[string]interface{}{
		"session": session, "photo": "shot.png", "exposure": 1, "highlights": -80, "sky": 60,
	}, &applied)
	if applied.Status != "edited" || applied.Edit.Highlights != -80 || applied.Edit.Sky != 60 {
		t.Fatalf("apply_edit = %+v", applied)
	}
	if _, err := os.Stat(filepath.Join(photoBaseDir, session, uneditedDirName, "shot.png")); err != nil {
		t.Fatalf("original not backed up: %v", err)
	}

	var analysis struct {
		Edit *photoEdit `json:"edit"`
		Tone frameStats `json:"tone"`
	}
	callTool(t, "analyze_photo", map[string]interface{}{"session": session, "photo": "shot.png", "include_image": false}, &analysis)
	if analysis.Edit == nil || analysis.Edit.Exposure != 1 {
		t.Errorf("analyze_photo should report the saved edit, got %+v", analysis.Edit)
	}
	if got := analysis.Tone.Regions["top"].ClippedPct; got != 0 {
		t.Errorf("saved photo's sky is clipped: %v%%", got)
	}

	callTool(t, "revert_edit", map[string]interface{}{"session": session, "photo": "shot.png"}, nil)
	if now, _ := os.ReadFile(photoPath); !bytes.Equal(now, pristine) {
		t.Fatal("revert_edit should restore the original bytes")
	}
}

func TestMCPToolErrorsAreResults(t *testing.T) {
	session := withTestSession(t, "shot.png", skySceneGrey(t, 200, 70, 96))
	cases := []map[string]interface{}{
		{"session": "../etc", "photo": "shot.png"},
		{"session": session, "photo": "../shot.png"},
		{"session": session, "photo": "missing.png"},
		{"session": session, "photo": "shot.png", "exposure": 9},
		{"session": session, "photo": "shot.png", "highlight": -50}, // misspelt
	}
	for _, args := range cases {
		if res := callTool(t, "preview_edit", args, nil); !res.IsError {
			t.Errorf("%v: expected a tool error, got %+v", args, res)
		}
	}
}

func TestMCPHTTPHandler(t *testing.T) {
	post := func(remote, origin, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		req.RemoteAddr = remote
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		mcpHTTPHandler(w, req)
		return w
	}
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`

	if w := post("127.0.0.1:5555", "", ping); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"result":{}`) {
		t.Errorf("loopback ping: %d %s", w.Code, w.Body)
	}
	if w := post("192.168.1.20:5555", "", ping); w.Code != http.StatusForbidden {
		t.Errorf("LAN client should be refused, got %d", w.Code)
	}
	if w := post("127.0.0.1:5555", "http://evil.example", ping); w.Code != http.StatusForbidden {
		t.Errorf("foreign origin should be refused, got %d", w.Code)
	}
	if w := post("[::1]:5555", "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); w.Code != http.StatusAccepted {
		t.Errorf("notification should be accepted with no body, got %d", w.Code)
	}
}

func TestMCPStdio(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
`)
	var out bytes.Buffer
	if err := serveMCPStdio(in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"2025-03-26"`) || !strings.Contains(lines[1], "apply_edit") {
		t.Fatalf("stdio replies = %q", lines)
	}
}
