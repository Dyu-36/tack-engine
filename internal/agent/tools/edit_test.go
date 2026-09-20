package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

type mockPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
}

func (m *mockPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	return true, nil
}
func (m *mockPermissionService) Grant(req permission.PermissionRequest) bool           { return true }
func (m *mockPermissionService) Deny(req permission.PermissionRequest) bool            { return true }
func (m *mockPermissionService) GrantPersistent(req permission.PermissionRequest) bool { return true }
func (m *mockPermissionService) AutoApproveSession(sessionID string)                   {}
func (m *mockPermissionService) SetSkipRequests(skip bool)                             {}
func (m *mockPermissionService) SkipRequests() bool                                    { return false }
func (m *mockPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

type mockHistoryService struct {
	*pubsub.Broker[history.File]
}

func (m *mockHistoryService) Create(ctx context.Context, sessionID, path, content string) (history.File, error) {
	return history.File{Path: path, Content: content}, nil
}
func (m *mockHistoryService) CreateVersion(ctx context.Context, sessionID, path, content string) (history.File, error) {
	return history.File{}, nil
}
func (m *mockHistoryService) GetByPathAndSession(ctx context.Context, path, sessionID string) (history.File, error) {
	return history.File{Path: path}, nil
}
func (m *mockHistoryService) Get(ctx context.Context, id string) (history.File, error) {
	return history.File{}, nil
}
func (m *mockHistoryService) ListBySession(ctx context.Context, sessionID string) ([]history.File, error) {
	return nil, nil
}
func (m *mockHistoryService) ListLatestSessionFiles(ctx context.Context, sessionID string) ([]history.File, error) {
	return nil, nil
}
func (m *mockHistoryService) Delete(ctx context.Context, id string) error { return nil }
func (m *mockHistoryService) DeleteSessionFiles(ctx context.Context, sessionID string) error {
	return nil
}

type mockEditFileTracker struct {
	lastRead time.Time
	reads    []string
}

func (m *mockEditFileTracker) RecordRead(ctx context.Context, sessionID, path string) {
	m.reads = append(m.reads, path)
}

func (m *mockEditFileTracker) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	return m.lastRead
}

func (m *mockEditFileTracker) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return m.reads, nil
}

func TestReplaceContentPreservesCRLFAndMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\r\nbeta\r\n"), 0o644))

	tracker := &mockEditFileTracker{lastRead: time.Now().Add(time.Second)}
	edit := editContext{
		ctx: context.WithValue(t.Context(), SessionIDContextKey, "session"),

		files:       &mockHistoryService{},
		filetracker: tracker,
		workingDir:  dir,
	}

	resp, err := replaceContent(edit, filePath, "beta", "BETA", false, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, "Content replaced in file: "+filePath, resp.Content)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\r\nBETA\r\n", string(content))
	require.Equal(t, []string{filePath}, tracker.reads)

	var meta EditResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, "alpha\nbeta\n", meta.OldContent)
	require.Equal(t, "alpha\r\nBETA\r\n", meta.NewContent)
}

func TestDeleteContentRejectsMultipleMatchesWithoutReplaceAll(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\nalpha\n"), 0o644))

	edit := editContext{
		ctx: context.WithValue(t.Context(), SessionIDContextKey, "session"),

		files:       &mockHistoryService{},
		filetracker: &mockEditFileTracker{lastRead: time.Now().Add(time.Second)},
		workingDir:  dir,
	}

	resp, err := deleteContent(edit, filePath, "alpha\n", false, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "old_string appears multiple times")

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\nbeta\nalpha\n", string(content))
}

func newMultiEditContext(t *testing.T, dir string) editContext {
	t.Helper()
	return editContext{
		ctx: context.WithValue(t.Context(), SessionIDContextKey, "session"),

		files:       &mockHistoryService{Broker: pubsub.NewBroker[history.File]()},
		filetracker: &mockEditFileTracker{lastRead: time.Now().Add(time.Second)},
		workingDir:  dir,
	}
}

func TestMultiEditAppliesAllUniqueEdits(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\ngamma\n"), 0o644))

	edit := newMultiEditContext(t, dir)
	resp, err := applyMultiEdit(edit, filePath, []EditOperation{
		{OldText: "alpha", NewText: "ALPHA"},
		{OldText: "gamma", NewText: "GAMMA"},
	}, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "Applied 2 edits")

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "ALPHA\nbeta\nGAMMA\n", string(content))
}

func TestMultiEditFailsWhenOldTextMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644))

	edit := newMultiEditContext(t, dir)
	resp, err := applyMultiEdit(edit, filePath, []EditOperation{
		{OldText: "alpha", NewText: "ALPHA"},
		{OldText: "does-not-exist", NewText: "x"},
	}, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "not found")

	// nothing written
	content, _ := os.ReadFile(filePath)
	require.Equal(t, "alpha\nbeta\n", string(content))
}

func TestMultiEditFailsWhenOldTextNotUnique(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\nalpha\n"), 0o644))

	edit := newMultiEditContext(t, dir)
	resp, err := applyMultiEdit(edit, filePath, []EditOperation{
		{OldText: "alpha", NewText: "ALPHA"},
	}, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "multiple times")

	content, _ := os.ReadFile(filePath)
	require.Equal(t, "alpha\nbeta\nalpha\n", string(content))
}

func TestMultiEditFailsOnOverlap(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("abcdefghij"), 0o644))

	edit := newMultiEditContext(t, dir)
	resp, err := applyMultiEdit(edit, filePath, []EditOperation{
		{OldText: "cdef", NewText: "X"},
		{OldText: "efgh", NewText: "Y"},
	}, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "overlaps")

	content, _ := os.ReadFile(filePath)
	require.Equal(t, "abcdefghij", string(content))
}

func TestMultiEditRejectsEmptyOldText(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\n"), 0o644))

	edit := newMultiEditContext(t, dir)
	resp, err := applyMultiEdit(edit, filePath, []EditOperation{
		{OldText: "", NewText: "x"},
	}, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "oldText must not be empty")
}
