package tools

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/filediff"
)

type editorInput struct {
	Path        *string
	Content     *string
	Edits       []filediff.Edit
	PendingEdit *pendingEdit
}

// pendingEdit is the one trailing edit object whose target is safely known but
// whose replacement string is still streaming. It is deliberately not a
// filediff.Edit: that type represents a complete handler argument.
type pendingEdit struct {
	OldText         string
	NewText         string
	NewTextComplete bool
}

// parsePartialEditorInput walks incomplete JSON structure with jsontext. The
// only manual component is decoding a valid prefix of a streaming write
// content or edit new_text string. Its exposed value contains complete logical
// lines only; edit targets remain complete structural values.
func parsePartialEditorInput(raw string) editorInput {
	d := jsontext.NewDecoder(strings.NewReader(raw), jsontext.AllowDuplicateNames(true))
	first, err := d.ReadToken()
	if err != nil || first.Kind() != '{' {
		return editorInput{}
	}
	var input editorInput
	for {
		tok, err := d.ReadToken()
		if err != nil {
			// A closed object that the structural decoder rejects (for
			// example a trailing comma) is malformed, not a usable prefix.
			if strings.HasSuffix(strings.TrimSpace(raw), "}") {
				return editorInput{}
			}
			return input
		}
		if tok.Kind() == '}' {
			return input
		}
		if tok.Kind() != '"' {
			return input
		}
		key := tok.String()
		switch key {
		case "path":
			input.Path = nil
		case "content":
			input.Content = nil
		case "edits":
			input.Edits, input.PendingEdit = nil, nil
		}
		valueStart := skipJSONValuePrefix(raw, int(d.InputOffset()))
		value, err := d.ReadValue()
		if err != nil {
			if key == "content" {
				if line, ok := completeJSONStringLines(raw, valueStart); ok {
					input.Content = &line
				}
			}
			if key == "edits" && valueStart <= len(raw) {
				input.Edits, input.PendingEdit = parseEditsPrefix(raw[valueStart:])
			}
			return input
		}
		switch key {
		case "path":
			if s, ok := decodeJSONString(value); ok {
				input.Path = &s
			}
		case "content":
			if s, ok := decodeJSONString(value); ok {
				input.Content = &s
			}
		case "edits":
			input.Edits, input.PendingEdit = parseEditsPrefix(string(value))
		}
		// jsontext infers commas and the next ReadToken validates the next
		// member; trailing commas therefore follow decoder semantics.
	}
}

func parseWriteFileInput(raw string) editorInput {
	if !json.Valid([]byte(raw)) {
		return parsePartialEditorInput(raw)
	}
	var args struct {
		Path    *string `json:"path"`
		Content *string `json:"content"`
	}
	if json.Unmarshal([]byte(raw), &args) != nil {
		return editorInput{}
	}
	return editorInput{Path: args.Path, Content: args.Content}
}

func parseEditFileInput(raw string) editorInput {
	if !json.Valid([]byte(raw)) {
		return parsePartialEditorInput(raw)
	}
	var args struct {
		Path  *string         `json:"path"`
		Edits []filediff.Edit `json:"edits"`
	}
	if json.Unmarshal([]byte(raw), &args) != nil {
		return editorInput{}
	}
	for _, edit := range args.Edits {
		if edit.NewText == nil {
			return editorInput{Path: args.Path}
		}
	}
	return editorInput{Path: args.Path, Edits: args.Edits}
}

func skipJSONValuePrefix(raw string, offset int) int {
	for offset < len(raw) {
		switch raw[offset] {
		case ':', ' ', '\t', '\r', '\n':
			offset++
		default:
			return offset
		}
	}
	return offset
}

func parseEditsPrefix(raw string) ([]filediff.Edit, *pendingEdit) {
	d := jsontext.NewDecoder(strings.NewReader(raw), jsontext.AllowDuplicateNames(true))
	tok, err := d.ReadToken()
	if err != nil || tok.Kind() != '[' {
		return nil, nil
	}
	var edits []filediff.Edit
	for {
		tok, err = d.ReadToken()
		if err != nil {
			if strings.HasSuffix(strings.TrimSpace(raw), "]") {
				return nil, nil
			}
			return edits, nil
		}
		if tok.Kind() == ']' {
			return edits, nil
		}
		if tok.Kind() != '{' {
			return edits, nil
		}
		edit, pending, status := parseEditObject(d, raw)
		switch status {
		case editPending:
			return edits, pending
		case editInvalid:
			// A closed object with missing/non-string required members is a
			// complete schema error. Do not skip it and preview a later edit;
			// encoding/json would reject the handler input as well.
			return nil, nil
		case editComplete:
			edits = append(edits, edit)
		}
	}
}

type editParse uint8

const (
	editPending editParse = iota
	editComplete
	editInvalid
)

func parseEditObject(d *jsontext.Decoder, raw string) (filediff.Edit, *pendingEdit, editParse) {
	var edit filediff.Edit
	var old, newText string
	var validOld, validNew, newTextComplete bool
	for {
		tok, err := d.ReadToken()
		if err != nil {
			return edit, makePendingEdit(old, newText, validOld, validNew, newTextComplete), editPending
		}
		if tok.Kind() == '}' {
			if validOld && validNew {
				edit.OldText = old
				edit.NewText = &newText
				return edit, nil, editComplete
			}
			return edit, nil, editInvalid
		}
		if tok.Kind() != '"' {
			return edit, nil, editPending
		}
		key := tok.String()
		switch key {
		case "old_text":
			validOld = false
		case "new_text":
			validNew, newTextComplete = false, false
		}
		valueStart := skipJSONValuePrefix(raw, int(d.InputOffset()))
		value, err := d.ReadValue()
		if err != nil {
			if key == "new_text" {
				if line, ok := completeJSONStringLines(raw, valueStart); ok {
					newText, validNew = line, true
				}
			}
			return edit, makePendingEdit(old, newText, validOld, validNew, newTextComplete), editPending
		}
		switch key {
		case "old_text":
			if s, ok := decodeJSONString(value); ok {
				old, validOld = s, true
			}
		case "new_text":
			if s, ok := decodeJSONString(value); ok {
				newText, validNew, newTextComplete = s, true, true
			}
		}
	}
}

func makePendingEdit(old, newText string, validOld, validNew, newTextComplete bool) *pendingEdit {
	if !validOld || !validNew {
		return nil
	}
	return &pendingEdit{OldText: old, NewText: newText, NewTextComplete: newTextComplete}
}

// completeJSONStringLines returns the whole logical lines already present in an
// incomplete JSON string that begins at raw[start] and runs to the end of raw.
func completeJSONStringLines(raw string, start int) (string, bool) {
	if start < 0 || start >= len(raw) || raw[start] != '"' {
		return "", false
	}
	var value string
	if json.Unmarshal([]byte(raw[start:]+`"`), &value) != nil {
		return "", false
	}
	if end := strings.LastIndex(value, "\n"); end >= 0 {
		return value[:end+1], true
	}
	return "", true
}

func decodeJSONString(value jsontext.Value) (string, bool) {
	var s *string
	if json.Unmarshal(value, &s) == nil && s != nil {
		return *s, true
	}
	return "", false
}

// writeFileInputReducer creates speculative write_file state from an append-only
// argument prefix. It deliberately does not acquire the mutation lock: a
// preview is informational only, while handlers remain authoritative.
func writeFileInputReducer(r *os.Root) agent.ToolInputReducer {
	return func(ctx context.Context, update agent.ToolInputUpdate, prior any) any {
		previous, _ := prior.(*filediff.State)
		input := parseWriteFileInput(editorRawInput(update))
		state, snapshot, ok := editorPreviewState(ctx, r, previous, input)
		if !ok {
			return state
		}
		if input.Content == nil {
			// An incomplete JSON escape can temporarily make the whole content
			// string undecodable. Keep the last valid preview while streaming;
			// final input remains authoritative and must not retain speculation.
			if !update.HasFinalInput {
				return preserveStreamingState(previous, state)
			}
			return state
		}
		content := *input.Content
		filePath := snapshot.Path
		if !update.HasFinalInput {
			if content == "" && !update.PreviewTruncated {
				return preserveStreamingState(previous, state)
			}
			bytes, hash := previewInputKey(content)
			if streamPreviewUnchanged(previous, filediff.PreviewWritePrefix, bytes, hash, update.PreviewTruncated) {
				return previous
			}
			prefixDiff := writePrefixDiff(filePath, content)
			state.Preview = &filediff.Preview{
				Kind:             filediff.PreviewWritePrefix,
				Diff:             &prefixDiff,
				InputPrefixBytes: bytes,
				InputPrefixHash:  hash,
				Pending:          true,
				InputTruncated:   update.PreviewTruncated,
			}
			return state
		}
		state.Phase = filediff.PhaseReady
		diff := filediff.Build("a/"+filePath, "b/"+filePath, snapshot.Raw, content)
		state.Preview = &filediff.Preview{Kind: filediff.PreviewWritePrefix, Diff: &diff, InputTruncated: update.PreviewTruncated}
		return state
	}
}

// editFileInputReducer creates speculative edit_file state from an append-only
// argument prefix. Like the write reducer it never acquires the mutation lock;
// the handler re-reads authoritatively beneath it.
func editFileInputReducer(r *os.Root) agent.ToolInputReducer {
	return func(ctx context.Context, update agent.ToolInputUpdate, prior any) any {
		previous, _ := prior.(*filediff.State)
		input := parseEditFileInput(editorRawInput(update))
		state, snapshot, ok := editorPreviewState(ctx, r, previous, input)
		if !ok {
			return state
		}
		if snapshot.State != filediff.SnapshotReady {
			return state
		}
		if _, ok := filediff.LineEndingStyle(strings.TrimPrefix(snapshot.Raw, bom)); !ok {
			state.Phase = filediff.PhaseUnavailable
			state.Reason = "file has inconsistent line endings"
			return state
		}
		filePath := snapshot.Path
		visiblePending := input.PendingEdit != nil && (input.PendingEdit.NewText != "" || input.PendingEdit.NewTextComplete)
		if len(input.Edits) == 0 && !visiblePending {
			if !update.HasFinalInput {
				return preserveStreamingState(previous, state)
			}
			return state
		}
		edits := append([]filediff.Edit(nil), input.Edits...)
		if visiblePending {
			newText := input.PendingEdit.NewText
			edits = append(edits, filediff.Edit{OldText: input.PendingEdit.OldText, NewText: &newText})
		}
		bytes, hash := editPreviewKey(edits)
		pending := !update.HasFinalInput
		if pending && streamPreviewUnchanged(previous, filediff.PreviewEdit, bytes, hash, update.PreviewTruncated) {
			return previous
		}
		replacements, err := filediff.MatchEdits(snapshot.Text, edits)
		if err != nil {
			// While streaming, a trailing edit whose target is not yet resolvable
			// must not discard an already-valid preview: earlier hunks would flicker
			// out and back in as input arrives. Keep the last streaming preview; a
			// genuine error still surfaces once the input is final (pending false).
			if pending {
				if preserveStreamingState(previous, state) == previous {
					return previous
				}
			}
			state.Reason = err.Error()
			return state
		}
		after := filediff.ApplyReplacements(snapshot.Text, replacements)
		diff := filediff.Build("a/"+filePath, "b/"+filePath, snapshot.Text, after)
		if pending {
			// Streamed input only ever adds content, so a frame that resolves fewer
			// lines than the previous one is a transient partial-JSON parse (e.g. a
			// chunk boundary inside a string escape). Keep the prior preview rather
			// than momentarily dropping a hunk.
			if regressesStreamingPreview(previous, diff) {
				return previous
			}
		} else {
			state.Phase = filediff.PhaseReady
		}
		state.Preview = &filediff.Preview{Kind: filediff.PreviewEdit, Diff: &diff, InputPrefixBytes: bytes, InputPrefixHash: hash, Pending: pending, InputTruncated: update.PreviewTruncated}
		return state
	}
}

func editorRawInput(update agent.ToolInputUpdate) string {
	if update.HasFinalInput {
		return update.FinalInput
	}
	return update.RawPrefix
}

// editorPreviewState validates the streamed path and captures (or reuses) the
// display snapshot shared by both editor reducers. When the returned bool is
// false the state is final and tool-specific rendering must not run.
func editorPreviewState(ctx context.Context, r *os.Root, previous *filediff.State, input editorInput) (*filediff.State, *filediff.Snapshot, bool) {
	if !usableWorkspacePath(input.Path) {
		return &filediff.State{Phase: filediff.PhaseStreaming, Reason: "waiting for a workspace-relative path"}, nil, false
	}
	filePath := path.Clean(*input.Path)
	snapshot := priorSnapshot(previous, filePath)
	if snapshot == nil {
		snapshot = captureEditorSnapshot(ctx, r, filePath)
	}
	state := &filediff.State{Phase: filediff.PhaseStreaming, Snapshot: snapshot}
	if snapshot.State == filediff.SnapshotUnavailable {
		state.Phase = filediff.PhaseUnavailable
		state.Reason = snapshot.Reason
		return state, snapshot, false
	}
	return state, snapshot, true
}

func preserveStreamingState(previous, next *filediff.State) *filediff.State {
	if previous == nil || previous.Phase != filediff.PhaseStreaming || previous.Snapshot == nil || next.Snapshot == nil || previous.Snapshot.Path != next.Snapshot.Path {
		return next
	}
	return previous
}

// regressesStreamingPreview reports whether a freshly built streaming diff
// resolved strictly fewer lines than the previous streaming preview. Streamed
// input only grows, so a shrink signals a transient partial parse rather than a
// real edit, and the previous preview should be retained to avoid a visible
// hunk flickering out and back in.
func regressesStreamingPreview(previous *filediff.State, diff filediff.Diff) bool {
	if previous == nil || previous.Phase != filediff.PhaseStreaming || previous.Preview == nil || previous.Preview.Diff == nil {
		return false
	}
	return diff.LineCount() < previous.Preview.Diff.LineCount()
}

func streamPreviewUnchanged(previous *filediff.State, kind filediff.PreviewKind, bytes int, hash uint64, truncated bool) bool {
	if previous == nil || previous.Preview == nil {
		return false
	}
	preview := previous.Preview
	return preview.Kind == kind && preview.Pending && preview.InputPrefixBytes == bytes && preview.InputPrefixHash == hash && preview.InputTruncated == truncated
}

func previewInputKey(value string) (int, uint64) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return len(value), h.Sum64()
}

func editPreviewKey(edits []filediff.Edit) (int, uint64) {
	h := fnv.New64a()
	bytes := 0
	for _, edit := range edits {
		bytes += len(edit.OldText) + 1
		_, _ = h.Write([]byte(edit.OldText))
		_, _ = h.Write([]byte{0})
		if edit.NewText != nil {
			bytes += len(*edit.NewText) + 1
			_, _ = h.Write([]byte(*edit.NewText))
		}
		_, _ = h.Write([]byte{0})
	}
	return bytes, h.Sum64()
}

func usableWorkspacePath(value *string) bool {
	if value == nil || *value == "" || strings.HasPrefix(*value, "/") {
		return false
	}
	clean := path.Clean(*value)
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

func priorSnapshot(previous *filediff.State, filePath string) *filediff.Snapshot {
	if previous == nil || previous.Snapshot == nil || previous.Snapshot.Path != filePath {
		return nil
	}
	copy := *previous.Snapshot
	return &copy
}

// captureEditorSnapshot reads an ordinary text file only. Failure to make a
// safe display snapshot never changes whether the eventual mutation may run.
func captureEditorSnapshot(ctx context.Context, r *os.Root, filePath string) *filediff.Snapshot {
	snapshot := &filediff.Snapshot{Path: filePath}
	info, err := r.Lstat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			snapshot.State = filediff.SnapshotMissing
			return snapshot
		}
		snapshot.State, snapshot.Reason = filediff.SnapshotUnavailable, err.Error()
		return snapshot
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		snapshot.State, snapshot.Reason = filediff.SnapshotUnavailable, "path is not a regular file"
		return snapshot
	}
	f, err := r.Open(filePath)
	if err != nil {
		snapshot.State, snapshot.Reason = filediff.SnapshotUnavailable, err.Error()
		return snapshot
	}
	defer func() { _ = f.Close() }()
	stopClose := context.AfterFunc(ctx, func() { _ = f.Close() })
	defer stopClose()
	// Validate the opened handle, not only the earlier path lookup. A path can
	// be replaced between Lstat and Open; never read an unexpected kind or a
	// file that grew beyond the preview cap.
	openedInfo, err := f.Stat()
	if err != nil {
		snapshot.State, snapshot.Reason = filediff.SnapshotUnavailable, err.Error()
		return snapshot
	}
	if !openedInfo.Mode().IsRegular() {
		snapshot.State, snapshot.Reason = filediff.SnapshotUnavailable, "path is not a regular file"
		return snapshot
	}
	if !os.SameFile(info, openedInfo) {
		snapshot.State, snapshot.Reason = filediff.SnapshotUnavailable, "file changed while opening"
		return snapshot
	}
	if openedInfo.Size() > maxFileSize {
		snapshot.State, snapshot.Reason = filediff.SnapshotUnavailable, "file exceeds 10 MB preview limit"
		return snapshot
	}
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: f}, maxFileSize+1))
	if err != nil || len(data) > maxFileSize || isBinary(data) {
		snapshot.State = filediff.SnapshotUnavailable
		if err != nil {
			snapshot.Reason = err.Error()
		} else if len(data) > maxFileSize {
			snapshot.Reason = "file exceeds 10 MB preview limit"
		} else {
			snapshot.Reason = "binary file"
		}
		return snapshot
	}
	text := strings.TrimPrefix(string(data), bom)
	// A write replacement is safe to render for any non-binary text, including
	// mixed endings or a literal bare CR. edit_file performs its stricter
	// line-ending validation only when it needs normalized matching text.
	snapshot.Raw, snapshot.Text, snapshot.State = string(data), filediff.NormalizeToLF(text), filediff.SnapshotReady
	return snapshot
}

func writePrefixDiff(filePath, content string) filediff.Diff {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	diff := filediff.Diff{From: "a/" + filePath, To: "b/" + filePath}
	hunk := filediff.Hunk{FromLine: 1, ToLine: 1}
	for number, line := range lines {
		hunk.NewCount++
		diff.Additions++
		hunk.Lines = append(hunk.Lines, filediff.DiffLine{Kind: filediff.LineAdd, NewNumber: number + 1, Content: filediff.LogicalLineContent(line)})
	}
	diff.Hunks = []filediff.Hunk{hunk}
	return diff
}

func appliedEditorState(filePath string, operation filediff.Op, diff filediff.Diff) *filediff.State {
	return &filediff.State{
		Phase:  filediff.PhaseApplied,
		Change: &filediff.Change{Path: filePath, Op: operation, State: filediff.ChangeApplied, Diff: &diff},
	}
}
