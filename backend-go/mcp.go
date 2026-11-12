package main

// An MCP (Model Context Protocol) server over the photo editor, so a local
// agent such as Claude Code can look at a photo, try adjustments, read back how
// much of the sky is clipped, and save the edit it settles on — the same
// non-destructive pipeline the web UI's editor drives.
//
// It is served two ways from the one dispatcher:
//
//   - Streamable HTTP at /mcp on the running server (stateless, JSON replies,
//     loopback clients only): `claude mcp add --transport http camera-rip
//     http://localhost:5001/mcp`.
//   - stdio, with `camera-rip -mcp`, for clients that launch their servers as a
//     subprocess. No web server is started in that mode.

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nfnt/resize"
)

const (
	mcpServerName    = "camera-rip"
	mcpServerVersion = "1.0.0"

	// The newest protocol revision this server speaks. A client asking for one
	// of mcpProtocolVersions gets it echoed back; anything else gets this.
	mcpLatestProtocol = "2025-11-25"

	// Longest side, in pixels, of a preview handed back to the agent. Big enough
	// to judge a sky and a bird against it, small enough to keep the image a
	// modest number of tokens. Callers may ask for anything in the range below.
	mcpDefaultPreviewSize = 1024
	mcpMinPreviewSize     = 256
	mcpMaxPreviewSize     = 2048

	// Longest side the tone statistics are measured at. Fixed, rather than tied
	// to the preview size, so numbers from different calls compare directly.
	mcpStatsSize = 1024

	// A channel at or above this value is counted as clipped, at or below
	// mcpCrushedLevel (all three channels) as crushed to black.
	mcpClippedLevel = 254
	mcpCrushedLevel = 2
)

var mcpProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
	"2025-11-25": true,
}

const mcpInstructions = `Camera Rip photo editor. Photos live in sessions (folders); every edit is
non-destructive and always rendered from the untouched original, so settings
never compound and revert_edit restores the original exactly.

Recovering a blown-out sky (typical: a bird exposed correctly against a bright sky):
1. list_sessions, then list_photos to find the photo.
2. analyze_photo on it. Look at the "top" region's clipped_pct and p99 and at
   the source stats: highlights already at 255 in the original JPEG carry no
   detail; you can only roll them off to a smooth tone, not recover texture.
3. preview_edit with candidate settings. Start with highlights -40…-80 (rolls the
   brightest tones off under white), then sky 30…70 with horizon at 100 for a
   bird against open sky or at the real horizon (percent of frame height) over
   a landscape. If that darkens the subject, raise shadows 20…50: it lifts only
   tones below the middle of the range and leaves the sky bit-for-bit alone.
   Use exposure (stops) only for the overall level; temperature/tint fix a cast.
4. Compare after.regions.top.clipped_pct (aim near 0) and after.p50 (the
   subject should not get much darker), look at the preview image, iterate.
5. apply_edit with the settings you chose. The web UI picks it up on reload.

Every setting is absolute (not relative to the current edit) and omitted ones
default to 0, i.e. no change. preview_edit with include_image=false is cheap.`

// mcpRequest is one JSON-RPC 2.0 message. A message without an id is a
// notification and gets no reply.
type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
)

// mcpContent is one block of a tool result: text, or a base64 image.
type mcpContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpTool struct {
	Name        string                 `json:"name"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
	Annotations map[string]interface{} `json:"annotations,omitempty"`
	call        func(args json.RawMessage) (mcpToolResult, error)
}

// handleMCPMessage answers one JSON-RPC message, returning nil for a
// notification or a stray response, which get no reply.
func handleMCPMessage(raw json.RawMessage) *mcpResponse {
	var req mcpRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &mcpError{Code: rpcParseError, Message: "Parse error"}}
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return nil // a notification (initialized, cancelled, …) or a response
	}
	reply := &mcpResponse{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" || req.Method == "" {
		reply.Error = &mcpError{Code: rpcInvalidRequest, Message: "Invalid request"}
		return reply
	}

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &params)
		version := mcpLatestProtocol
		if mcpProtocolVersions[params.ProtocolVersion] {
			version = params.ProtocolVersion
		}
		reply.Result = map[string]interface{}{
			"protocolVersion": version,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]string{"name": mcpServerName, "version": mcpServerVersion},
			"instructions":    mcpInstructions,
		}
	case "ping":
		reply.Result = map[string]interface{}{}
	case "tools/list":
		reply.Result = map[string]interface{}{"tools": mcpTools()}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			reply.Error = &mcpError{Code: rpcInvalidParams, Message: "Invalid params"}
			return reply
		}
		tool := findMCPTool(params.Name)
		if tool == nil {
			reply.Error = &mcpError{Code: rpcInvalidParams, Message: "Unknown tool: " + params.Name}
			return reply
		}
		args := params.Arguments
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage("{}")
		}
		result, err := tool.call(args)
		if err != nil {
			// Tool failures go back as a result the model can read and act on,
			// not as a protocol error.
			result = mcpToolResult{IsError: true, Content: []mcpContent{{Type: "text", Text: err.Error()}}}
		}
		reply.Result = result
	default:
		reply.Error = &mcpError{Code: rpcMethodNotFound, Message: "Method not found: " + req.Method}
	}
	return reply
}

// handleMCPPayload answers a body that is either one message or a batch of
// them. It returns nil when nothing in it wants a reply.
func handleMCPPayload(body []byte) interface{} {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			return &mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &mcpError{Code: rpcParseError, Message: "Parse error"}}
		}
		replies := []*mcpResponse{}
		for _, msg := range batch {
			if r := handleMCPMessage(msg); r != nil {
				replies = append(replies, r)
			}
		}
		if len(replies) == 0 {
			return nil
		}
		return replies
	}
	if r := handleMCPMessage(body); r != nil {
		return r
	}
	return nil
}

// isLoopbackRequest reports whether r came from this machine. The MCP endpoint
// can rewrite photos, so unlike the web UI it is not offered to the LAN.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// allowedMCPOrigin rejects browser requests from any page but one served from
// this machine, which is what stops a malicious web page from reaching the
// endpoint through DNS rebinding. Non-browser clients send no Origin at all.
func allowedMCPOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// mcpHTTPHandler is the Streamable HTTP transport, in its stateless form: each
// POST carries a message and gets its reply as plain JSON. There is no server
// to client stream, so GET is refused as the specification allows.
func mcpHTTPHandler(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) || !allowedMCPOrigin(r.Header.Get("Origin")) {
		writeJSONError(w, http.StatusForbidden, "The MCP endpoint only accepts connections from this machine")
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK) // no sessions to end
		return
	default:
		w.Header().Set("Allow", "POST, DELETE")
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}
	reply := handleMCPPayload(body)
	if reply == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reply)
}

// serveMCPStdio runs the stdio transport: newline-delimited JSON-RPC on stdin
// and stdout. Logging already goes to stderr, which keeps stdout clean.
func serveMCPStdio(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if reply := handleMCPPayload(line); reply != nil {
			if err := enc.Encode(reply); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

// --- Tools ---------------------------------------------------------------

// editSettingsSchema is the JSON Schema of the adjustments shared by
// preview_edit and apply_edit, with the same ranges /api/edit-photo enforces.
func editSettingsSchema() map[string]interface{} {
	num := func(min, max float64, desc string) map[string]interface{} {
		return map[string]interface{}{"type": "number", "minimum": min, "maximum": max, "description": desc}
	}
	return map[string]interface{}{
		"exposure":    num(-5, 5, "Exposure shift in stops (linear light). Negative darkens the whole frame."),
		"highlights":  num(-100, 100, "Highlight shoulder. Negative rolls the brightest tones off under white — the first tool for a clipping sky."),
		"shadows":     num(0, 100, "Shadow lift. Opens up tones below the middle of the range and cannot touch anything brighter, so it brightens a dark bird without touching the sky."),
		"sky":         num(0, 100, "Graduated sky pull: up to 2 stops compressed towards middle grey from the top of the frame down to the horizon. Deep shadows are untouched, midtones nearly so."),
		"horizon":     num(0, 100, "How far down the frame (percent of height) the sky pull reaches before easing out. 100 (the default when 0) covers the whole frame evenly."),
		"black":       num(-100, 100, "Black point. Positive crushes shadows / adds contrast, negative lifts them."),
		"temperature": num(-100, 100, "White balance. Negative cools (bluer), positive warms."),
		"tint":        num(-100, 100, "White balance. Negative towards green, positive towards magenta."),
		"crop": map[string]interface{}{
			"type":        "object",
			"description": "Optional crop in fractions (0-1) of the upright photo. Omit for no crop.",
			"properties": map[string]interface{}{
				"x": map[string]interface{}{"type": "number"},
				"y": map[string]interface{}{"type": "number"},
				"w": map[string]interface{}{"type": "number"},
				"h": map[string]interface{}{"type": "number"},
			},
			"required": []string{"x", "y", "w", "h"},
		},
	}
}

func photoTargetSchema(extra map[string]interface{}, required ...string) map[string]interface{} {
	props := map[string]interface{}{
		"session": map[string]interface{}{"type": "string", "description": "Session folder name, from list_sessions."},
		"photo":   map[string]interface{}{"type": "string", "description": "Photo filename, from list_photos."},
	}
	for k, v := range extra {
		props[k] = v
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": props,
		"required":   append([]string{"session", "photo"}, required...),
	}
}

func mcpTools() []mcpTool {
	readOnly := map[string]interface{}{"readOnlyHint": true, "openWorldHint": false}
	previewSize := map[string]interface{}{
		"type": "integer", "minimum": mcpMinPreviewSize, "maximum": mcpMaxPreviewSize,
		"description": fmt.Sprintf("Longest side of the returned image in pixels (default %d).", mcpDefaultPreviewSize),
	}
	includeImage := map[string]interface{}{
		"type": "boolean", "description": "Return the image as well as the statistics (default true).",
	}

	previewProps := editSettingsSchema()
	previewProps["max_size"] = previewSize
	previewProps["include_image"] = includeImage

	analyzeProps := map[string]interface{}{
		"max_size":      previewSize,
		"include_image": includeImage,
		"original": map[string]interface{}{
			"type": "boolean", "description": "Analyze the unedited original instead of the photo as it now stands (default false).",
		},
	}

	return []mcpTool{
		{
			Name:        "list_sessions",
			Title:       "List photo sessions",
			Description: "List photo sessions (import folders), newest first, with how many photos each holds and how many were selected.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			Annotations: readOnly,
			call:        mcpListSessions,
		},
		{
			Name:        "list_photos",
			Title:       "List photos in a session",
			Description: "List the photos in a session with whether each is selected, whether it can be edited (JPEG/PNG) and its current edit settings if it has been edited.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session":       map[string]interface{}{"type": "string", "description": "Session folder name, from list_sessions."},
					"selected_only": map[string]interface{}{"type": "boolean", "description": "Only list photos the user selected (default false)."},
				},
				"required": []string{"session"},
			},
			Annotations: readOnly,
			call:        mcpListPhotos,
		},
		{
			Name:  "analyze_photo",
			Title: "Look at a photo and measure its tones",
			Description: "Return a downscaled view of a photo plus tone statistics: luminance percentiles, clipped/crushed percentages, " +
				"mean colour and a 16-bin histogram, for the whole frame and its top, middle and bottom thirds (the top third is usually the sky). " +
				"Also returns the current edit settings.",
			InputSchema: photoTargetSchema(analyzeProps),
			Annotations: readOnly,
			call:        mcpAnalyzePhoto,
		},
		{
			Name:  "preview_edit",
			Title: "Preview an edit without saving it",
			Description: "Render the given settings over the untouched original and return the result with before/after tone statistics. " +
				"Nothing is written to disk. Settings are absolute; omitted ones mean no change.",
			InputSchema: photoTargetSchema(previewProps),
			Annotations: readOnly,
			call:        mcpPreviewEdit,
		},
		{
			Name:  "apply_edit",
			Title: "Save an edit",
			Description: "Apply the given settings to the photo, rendered from the untouched original, and save it in place (the original is kept " +
				"in the session's unedited/ folder and the selected/ copy is refreshed). Replaces any earlier edit. All-zero settings revert.",
			InputSchema: photoTargetSchema(editSettingsSchema()),
			Annotations: map[string]interface{}{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
			call:        mcpApplyEdit,
		},
		{
			Name:        "revert_edit",
			Title:       "Restore the original",
			Description: "Discard a photo's edit and put the untouched original back.",
			InputSchema: photoTargetSchema(nil),
			Annotations: map[string]interface{}{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true, "openWorldHint": false},
			call:        mcpRevertEdit,
		},
	}
}

func findMCPTool(name string) *mcpTool {
	for _, t := range mcpTools() {
		if t.Name == name {
			t := t
			return &t
		}
	}
	return nil
}

func textResult(v interface{}) (mcpToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcpToolResult{}, err
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(data)}}}, nil
}

// decodeArgs unmarshals tool arguments strictly, so a misspelt setting is an
// error the agent sees rather than an adjustment silently left at zero.
func decodeArgs(args json.RawMessage, v interface{}) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

func checkSession(session string) error {
	if !validDirName(session) {
		return fmt.Errorf("invalid session name %q", session)
	}
	dir, err := safePhotoPath(session)
	if err != nil {
		return fmt.Errorf("invalid session name %q", session)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("session %q not found", session)
	}
	return nil
}

func mcpListSessions(args json.RawMessage) (mcpToolResult, error) {
	entries, err := os.ReadDir(photoBaseDir)
	if err != nil {
		return mcpToolResult{}, fmt.Errorf("failed to read the photo folder: %v", err)
	}
	dirs := []sessionDirectory{}
	for _, entry := range entries {
		if !entry.IsDir() || !validDirName(entry.Name()) {
			continue
		}
		dir := filepath.Join(photoBaseDir, entry.Name())
		dirs = append(dirs, sessionDirectory{
			Name:          entry.Name(),
			PhotoCount:    countPhotoFiles(dir),
			SelectedCount: countPhotoFiles(filepath.Join(dir, "selected")),
		})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name > dirs[j].Name })
	return textResult(dirs)
}

type mcpPhotoEntry struct {
	Name     string     `json:"name"`
	Selected bool       `json:"selected"`
	Editable bool       `json:"editable"`
	Edit     *photoEdit `json:"edit,omitempty"`
}

func mcpListPhotos(args json.RawMessage) (mcpToolResult, error) {
	var req struct {
		Session      string `json:"session"`
		SelectedOnly bool   `json:"selected_only"`
	}
	if err := decodeArgs(args, &req); err != nil {
		return mcpToolResult{}, err
	}
	if err := checkSession(req.Session); err != nil {
		return mcpToolResult{}, err
	}
	dir, _ := safePhotoPath(req.Session)
	photos, err := listPhotoFiles(dir)
	if err != nil {
		return mcpToolResult{}, fmt.Errorf("failed to list photos: %v", err)
	}
	selected := map[string]bool{}
	if names, err := listPhotoFiles(filepath.Join(dir, "selected")); err == nil {
		for _, n := range names {
			selected[n] = true
		}
	}
	editsMu.Lock()
	edits := readEdits(req.Session)
	editsMu.Unlock()
	backups := backedUpOriginals(req.Session)

	out := []mcpPhotoEntry{}
	for _, name := range photos {
		if req.SelectedOnly && !selected[name] {
			continue
		}
		entry := mcpPhotoEntry{Name: name, Selected: selected[name], Editable: isEditableImage(name)}
		if e, ok := edits[name]; ok {
			e := e
			entry.Edit = &e
		} else if backups[name] {
			entry.Edit = &photoEdit{}
		}
		out = append(out, entry)
	}
	return textResult(map[string]interface{}{"session": req.Session, "count": len(out), "photos": out})
}

// currentEdit returns the recorded settings for a photo, or nil if unedited.
func currentEdit(session, photo string) *photoEdit {
	editsMu.Lock()
	defer editsMu.Unlock()
	if e, ok := readEdits(session)[photo]; ok {
		return &e
	}
	if backedUpOriginals(session)[photo] {
		return &photoEdit{}
	}
	return nil
}

// loadUpright decodes a photo, bakes in its EXIF orientation and shrinks it so
// its longest side is at most maxSide.
func loadUpright(data []byte, maxSide int) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("could not decode the photo: %v", err)
	}
	img = applyOrientation(img, exifOrientation(data))
	b := img.Bounds()
	if b.Dx() > maxSide || b.Dy() > maxSide {
		img = resize.Thumbnail(uint(maxSide), uint(maxSide), img, resize.Bilinear)
	}
	return img, nil
}

// renderPreview runs the edit pipeline over an already-shrunk upright frame.
// Every adjustment is per pixel or relative to frame height, so tone-then-crop
// on the small frame matches the full-size render.
func renderPreview(upright image.Image, edit photoEdit) image.Image {
	out := image.NewRGBA(upright.Bounds().Sub(upright.Bounds().Min))
	src := toRGBA(upright)
	copy(out.Pix, src.Pix)
	applyTone(out, edit)
	if edit.Crop != nil && !edit.Crop.isFull() {
		return toRGBA(cropImage(out, *edit.Crop))
	}
	return out
}

func encodePreview(img image.Image, maxSide int) (mcpContent, error) {
	b := img.Bounds()
	if b.Dx() > maxSide || b.Dy() > maxSide {
		img = resize.Thumbnail(uint(maxSide), uint(maxSide), img, resize.Bilinear)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return mcpContent{}, err
	}
	return mcpContent{Type: "image", MimeType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(buf.Bytes())}, nil
}

// toneStats summarises one region of a rendered frame. Luminance is Rec. 709
// on the 8-bit display values; percentages are of the region's pixels.
type toneStats struct {
	Pixels      int        `json:"pixels"`
	MeanLuma    float64    `json:"mean_luma"`
	P1          int        `json:"p1"`
	P5          int        `json:"p5"`
	P50         int        `json:"p50"`
	P95         int        `json:"p95"`
	P99         int        `json:"p99"`
	ClippedPct  float64    `json:"clipped_pct"`  // any channel >= 254
	AllClipPct  float64    `json:"all_clip_pct"` // all channels >= 254 (pure white)
	CrushedPct  float64    `json:"crushed_pct"`  // all channels <= 2
	MeanRGB     [3]float64 `json:"mean_rgb"`
	Histogram16 []float64  `json:"histogram_16,omitempty"` // luminance, percent per bin
}

type frameStats struct {
	Width   int                   `json:"width"`
	Height  int                   `json:"height"`
	Whole   toneStats             `json:"whole"`
	Regions map[string]*toneStats `json:"regions"`
}

type statsAccumulator struct {
	hist                 [256]int
	n, clipped, allClip  int
	crushed              int
	sumR, sumG, sumB, sY float64
}

func (a *statsAccumulator) add(r, g, b uint8) {
	y := 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)
	a.hist[int(math.Round(y))]++
	a.n++
	a.sY += y
	a.sumR += float64(r)
	a.sumG += float64(g)
	a.sumB += float64(b)
	if r >= mcpClippedLevel || g >= mcpClippedLevel || b >= mcpClippedLevel {
		a.clipped++
	}
	if r >= mcpClippedLevel && g >= mcpClippedLevel && b >= mcpClippedLevel {
		a.allClip++
	}
	if r <= mcpCrushedLevel && g <= mcpCrushedLevel && b <= mcpCrushedLevel {
		a.crushed++
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (a *statsAccumulator) stats(withHistogram bool) toneStats {
	s := toneStats{Pixels: a.n}
	if a.n == 0 {
		return s
	}
	n := float64(a.n)
	pct := func(c int) float64 { return round2(100 * float64(c) / n) }
	percentile := func(p float64) int {
		target := int(math.Ceil(p * n))
		if target < 1 {
			target = 1
		}
		seen := 0
		for v, c := range a.hist {
			seen += c
			if seen >= target {
				return v
			}
		}
		return 255
	}
	s.MeanLuma = round2(a.sY / n)
	s.P1, s.P5, s.P50, s.P95, s.P99 = percentile(0.01), percentile(0.05), percentile(0.5), percentile(0.95), percentile(0.99)
	s.ClippedPct, s.AllClipPct, s.CrushedPct = pct(a.clipped), pct(a.allClip), pct(a.crushed)
	s.MeanRGB = [3]float64{round2(a.sumR / n), round2(a.sumG / n), round2(a.sumB / n)}
	if withHistogram {
		s.Histogram16 = make([]float64, 16)
		for v, c := range a.hist {
			s.Histogram16[v/16] += float64(c)
		}
		for i := range s.Histogram16 {
			s.Histogram16[i] = round2(100 * s.Histogram16[i] / n)
		}
	}
	return s
}

// measureTones computes statistics for a frame as a whole and for its top,
// middle and bottom thirds.
func measureTones(img image.Image) frameStats {
	rgba := toRGBA(img)
	b := rgba.Bounds()
	var whole statsAccumulator
	thirds := [3]statsAccumulator{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		band := 3 * (y - b.Min.Y) / b.Dy()
		row := rgba.Pix[rgba.PixOffset(b.Min.X, y) : rgba.PixOffset(b.Min.X, y)+b.Dx()*4]
		for i := 0; i+3 < len(row); i += 4 {
			whole.add(row[i], row[i+1], row[i+2])
			thirds[band].add(row[i], row[i+1], row[i+2])
		}
	}
	regions := map[string]*toneStats{}
	for i, name := range []string{"top", "middle", "bottom"} {
		s := thirds[i].stats(false)
		regions[name] = &s
	}
	return frameStats{Width: b.Dx(), Height: b.Dy(), Whole: whole.stats(true), Regions: regions}
}

type photoArgs struct {
	Session      string `json:"session"`
	Photo        string `json:"photo"`
	MaxSize      int    `json:"max_size"`
	IncludeImage *bool  `json:"include_image"`
}

func (p photoArgs) validate() error {
	if err := checkSession(p.Session); err != nil {
		return err
	}
	if !validPhotoName(p.Photo) {
		return fmt.Errorf("invalid photo name %q", p.Photo)
	}
	return nil
}

func (p photoArgs) previewSize() int {
	if p.MaxSize == 0 {
		return mcpDefaultPreviewSize
	}
	return int(math.Max(mcpMinPreviewSize, math.Min(mcpMaxPreviewSize, float64(p.MaxSize))))
}

func (p photoArgs) wantImage() bool { return p.IncludeImage == nil || *p.IncludeImage }

// statsFrame shrinks a rendered frame to the fixed statistics size.
func statsFrame(img image.Image) image.Image {
	b := img.Bounds()
	if b.Dx() > mcpStatsSize || b.Dy() > mcpStatsSize {
		return resize.Thumbnail(mcpStatsSize, mcpStatsSize, img, resize.Bilinear)
	}
	return img
}

func mcpAnalyzePhoto(args json.RawMessage) (mcpToolResult, error) {
	var req struct {
		photoArgs
		Original bool `json:"original"`
	}
	if err := decodeArgs(args, &req); err != nil {
		return mcpToolResult{}, err
	}
	if err := req.validate(); err != nil {
		return mcpToolResult{}, err
	}

	var data []byte
	var err error
	if req.Original {
		data, err = readOriginal(req.Session, req.Photo)
	} else {
		var path string
		path, err = safePhotoPath(req.Session, req.Photo)
		if err == nil {
			data, err = os.ReadFile(path)
			if os.IsNotExist(err) {
				err = fmt.Errorf("photo %q not found in %q", req.Photo, req.Session)
			}
		}
	}
	if err != nil {
		return mcpToolResult{}, err
	}

	size := req.previewSize()
	if size < mcpStatsSize {
		size = mcpStatsSize
	}
	img, err := loadUpright(data, size)
	if err != nil {
		return mcpToolResult{}, err
	}

	report := map[string]interface{}{
		"session":   req.Session,
		"photo":     req.Photo,
		"analyzed":  map[bool]string{true: "original", false: "current"}[req.Original],
		"editable":  isEditableImage(req.Photo),
		"edit":      currentEdit(req.Session, req.Photo),
		"tone":      measureTones(statsFrame(img)),
		"clip_note": "clipped_pct counts pixels with any channel >= 254; all_clip_pct pixels blown to pure white.",
	}
	result, err := textResult(report)
	if err != nil || !req.wantImage() {
		return result, err
	}
	content, err := encodePreview(img, req.previewSize())
	if err != nil {
		return mcpToolResult{}, err
	}
	result.Content = append(result.Content, content)
	return result, nil
}

type editArgs struct {
	photoArgs
	Crop        *cropRect `json:"crop"`
	Temperature float64   `json:"temperature"`
	Tint        float64   `json:"tint"`
	Exposure    float64   `json:"exposure"`
	Black       float64   `json:"black"`
	Highlights  float64   `json:"highlights"`
	Shadows     float64   `json:"shadows"`
	Sky         float64   `json:"sky"`
	Horizon     float64   `json:"horizon"`
}

func (a editArgs) edit() photoEdit {
	return photoEdit{
		Crop:        a.Crop,
		Temperature: a.Temperature,
		Tint:        a.Tint,
		Exposure:    a.Exposure,
		Black:       a.Black,
		Highlights:  a.Highlights,
		Shadows:     a.Shadows,
		Sky:         a.Sky,
		Horizon:     a.Horizon,
	}
}

func mcpPreviewEdit(args json.RawMessage) (mcpToolResult, error) {
	var req editArgs
	if err := decodeArgs(args, &req); err != nil {
		return mcpToolResult{}, err
	}
	if err := req.validate(); err != nil {
		return mcpToolResult{}, err
	}
	edit := req.edit()
	if err := validateEdit(req.Photo, edit); err != nil {
		return mcpToolResult{}, err
	}
	original, err := readOriginal(req.Session, req.Photo)
	if err != nil {
		return mcpToolResult{}, err
	}

	size := req.previewSize()
	if size < mcpStatsSize {
		size = mcpStatsSize
	}
	upright, err := loadUpright(original, size)
	if err != nil {
		return mcpToolResult{}, err
	}
	rendered := renderPreview(upright, edit)

	// "before" is the original with only the crop applied, so both sets of
	// numbers describe the same pixels.
	before := upright
	if edit.Crop != nil && !edit.Crop.isFull() {
		before = cropImage(upright, *edit.Crop)
	}

	result, err := textResult(map[string]interface{}{
		"session":  req.Session,
		"photo":    req.Photo,
		"settings": edit,
		"saved":    false,
		"before":   measureTones(statsFrame(before)),
		"after":    measureTones(statsFrame(rendered)),
	})
	if err != nil || !req.wantImage() {
		return result, err
	}
	content, err := encodePreview(rendered, req.previewSize())
	if err != nil {
		return mcpToolResult{}, err
	}
	result.Content = append(result.Content, content)
	return result, nil
}

func mcpApplyEdit(args json.RawMessage) (mcpToolResult, error) {
	var req editArgs
	if err := decodeArgs(args, &req); err != nil {
		return mcpToolResult{}, err
	}
	if req.MaxSize != 0 || req.IncludeImage != nil {
		return mcpToolResult{}, fmt.Errorf("apply_edit does not take max_size or include_image; call analyze_photo afterwards to look at the result")
	}
	if err := req.validate(); err != nil {
		return mcpToolResult{}, err
	}
	saved, reverted, err := applyPhotoEdit(req.Session, req.Photo, req.edit())
	if err != nil {
		return mcpToolResult{}, err
	}
	if reverted {
		return textResult(map[string]interface{}{"status": "reverted", "photo": req.Photo,
			"note": "All settings were neutral, so the original was restored."})
	}
	log.Printf("MCP: edited %s/%s", req.Session, req.Photo)
	return textResult(map[string]interface{}{"status": "edited", "photo": req.Photo, "edit": saved,
		"note": "Saved. Reload the photo in the web UI to see it."})
}

func mcpRevertEdit(args json.RawMessage) (mcpToolResult, error) {
	var req struct {
		Session string `json:"session"`
		Photo   string `json:"photo"`
	}
	if err := decodeArgs(args, &req); err != nil {
		return mcpToolResult{}, err
	}
	if err := (photoArgs{Session: req.Session, Photo: req.Photo}).validate(); err != nil {
		return mcpToolResult{}, err
	}
	if err := revertPhoto(req.Session, req.Photo); err != nil {
		return mcpToolResult{}, err
	}
	log.Printf("MCP: reverted %s/%s", req.Session, req.Photo)
	return textResult(map[string]interface{}{"status": "reverted", "photo": req.Photo})
}

// runMCPStdio is the -mcp entry point.
func runMCPStdio() {
	log.SetOutput(os.Stderr)
	if err := serveMCPStdio(os.Stdin, os.Stdout); err != nil {
		log.Fatalf("MCP stdio transport failed: %v", err)
	}
}

// mcpUsage is printed with the server banner so the endpoint is discoverable.
func mcpUsage(port string) string {
	return strings.Join([]string{
		"  Agent (MCP):       http://localhost:" + port + "/mcp",
		"      claude mcp add --transport http camera-rip http://localhost:" + port + "/mcp",
	}, "\n")
}
