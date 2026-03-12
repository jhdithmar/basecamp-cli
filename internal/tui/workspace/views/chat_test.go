package views

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func testChatPool() *data.Pool[data.ChatLinesResult] {
	return data.NewPool[data.ChatLinesResult](
		"chat:test",
		data.PoolConfig{},
		func(context.Context) (data.ChatLinesResult, error) {
			return data.ChatLinesResult{}, nil
		},
	)
}

func TestChat_PoolUpdatedSetsBoostTargetToLatestLine(t *testing.T) {
	pool := testChatPool()
	pool.Set(data.ChatLinesResult{
		Lines: []data.ChatLineInfo{
			{ID: 100, Body: "one", Creator: "Alice", CreatedAt: "9:00am"},
			{ID: 200, Body: "two", Creator: "Bob", CreatedAt: "9:01am"},
			{ID: 300, Body: "three", Creator: "Carol", CreatedAt: "9:02am"},
		},
		TotalCount: 3,
	})

	v := &Chat{
		pool:           pool,
		styles:         tui.NewStyles(),
		viewport:       viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		selectedLineID: 100, // stale target before refresh
		lastID:         100,
	}

	model, cmd := v.Update(data.PoolUpdatedMsg{Key: pool.Key()})
	require.NotNil(t, model)
	assert.Nil(t, cmd)
	assert.Equal(t, int64(300), v.selectedLineID, "pool updates should retarget boost to the newest line")
}

func TestChat_ScrollModeBoostHotkeyOpensPickerForSelectedLine(t *testing.T) {
	session := workspace.NewTestSession()
	session.SetScope(workspace.Scope{ProjectID: 42})

	v := &Chat{
		session:        session,
		keys:           defaultChatKeyMap(),
		mode:           chatModeScroll,
		lines:          []workspace.ChatLineInfo{{ID: 10}, {ID: 20}},
		selectedLineID: 20,
	}

	for _, r := range []rune{'b', 'B'} {
		cmd := v.handleScrollKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		require.NotNil(t, cmd, "expected boost cmd for %q", string(r))

		msg := cmd()
		open, ok := msg.(workspace.OpenBoostPickerMsg)
		require.True(t, ok, "expected OpenBoostPickerMsg for %q", string(r))
		assert.Equal(t, int64(42), open.Target.ProjectID)
		assert.Equal(t, int64(20), open.Target.RecordingID)
		assert.Equal(t, "Chat line", open.Target.Title)
	}
}

func TestWrapLine_Unicode(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  string
	}{
		{
			name:  "ASCII fits",
			line:  "hello world",
			width: 20,
			want:  "hello world",
		},
		{
			name:  "ASCII wraps",
			line:  "hello world foo",
			width: 11,
			want:  "hello world\nfoo",
		},
		{
			name:  "emoji display width",
			line:  "🎉🎊🎈 party time celebrations",
			width: 15,
			want:  "🎉🎊🎈 party\ntime\ncelebrations",
		},
		{
			name:  "long emoji word",
			line:  "🎉🎊🎈🎆🎇🧨✨🎃",
			width: 4,
			want:  "🎉🎊\n🎈🎆\n🎇🧨\n✨🎃",
		},
		{
			name:  "accented characters",
			line:  "café résumé naïve",
			width: 10,
			want:  "café\nrésumé\nnaïve",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapLine(tt.line, tt.width)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWrapLine_HyperlinkTruncation(t *testing.T) {
	longURL := "https://example.com/" + strings.Repeat("a", 80)
	word := ansi.SetHyperlink(longURL) + longURL + ansi.ResetHyperlink()

	got := wrapLine(word, 30)

	assert.LessOrEqual(t, ansi.StringWidth(got), 30,
		"visible width must not exceed the wrap width")
	assert.Contains(t, got, "\x1b]8;;"+longURL+"\x07",
		"OSC 8 opener with full URL must be preserved")
	assert.Contains(t, got, "\x1b]8;;\x07",
		"OSC 8 reset sequence must be present")
}

func testChatWithLines(lines []workspace.ChatLineInfo) *Chat {
	pool := testChatPool()
	return &Chat{
		pool:     pool,
		styles:   tui.NewStyles(),
		viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		lines:    lines,
		width:    80,
		height:   20,
	}
}

func TestChat_MessageGrouping(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	v := testChatWithLines([]workspace.ChatLineInfo{
		{ID: 1, Body: "hello", Creator: "Alice", CreatedAt: "9:00am", CreatedAtTS: now},
		{ID: 2, Body: "world", Creator: "Alice", CreatedAt: "9:00am", CreatedAtTS: now.Add(30 * time.Second)},
		{ID: 3, Body: "again", Creator: "Alice", CreatedAt: "9:01am", CreatedAtTS: now.Add(60 * time.Second)},
	})

	v.renderMessages()
	content := v.viewport.View()

	// "Alice" should appear exactly once — grouped header
	assert.Equal(t, 1, strings.Count(content, "Alice"),
		"consecutive messages from same sender within 5 min should show one header")
}

func TestChat_DifferentSender_BreaksGroup(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	v := testChatWithLines([]workspace.ChatLineInfo{
		{ID: 1, Body: "hi", Creator: "Alice", CreatedAt: "9:00am", CreatedAtTS: now},
		{ID: 2, Body: "hey", Creator: "Bob", CreatedAt: "9:01am", CreatedAtTS: now.Add(time.Minute)},
	})

	v.renderMessages()
	content := v.viewport.View()

	assert.Contains(t, content, "Alice")
	assert.Contains(t, content, "Bob")
}

func TestChat_DateSeparator(t *testing.T) {
	// Use dates far enough in the past that neither is "Today" or "Yesterday"
	day1 := time.Date(2025, 6, 10, 14, 0, 0, 0, time.UTC)
	day2 := time.Date(2025, 6, 11, 9, 0, 0, 0, time.UTC)

	v := testChatWithLines([]workspace.ChatLineInfo{
		{ID: 1, Body: "old msg", Creator: "Alice", CreatedAt: "2:00pm", CreatedAtTS: day1},
		{ID: 2, Body: "new msg", Creator: "Alice", CreatedAt: "9:00am", CreatedAtTS: day2},
	})

	v.renderMessages()
	content := v.viewport.View()

	// Should have two date headers (one per day)
	assert.Contains(t, content, "Jun 10", "should show date for first day")
	assert.Contains(t, content, "Jun 11", "should show date for second day")
}

func TestChat_MidnightBoundary_ForcesHeader(t *testing.T) {
	// Same sender, within 5 minutes, but crossing local midnight — header should still appear
	beforeMidnight := time.Date(2025, 6, 10, 23, 58, 0, 0, time.Local)
	afterMidnight := time.Date(2025, 6, 11, 0, 1, 0, 0, time.Local)

	v := testChatWithLines([]workspace.ChatLineInfo{
		{ID: 1, Body: "late night", Creator: "Alice", CreatedAt: "11:58pm", CreatedAtTS: beforeMidnight},
		{ID: 2, Body: "early morning", Creator: "Alice", CreatedAt: "12:01am", CreatedAtTS: afterMidnight},
	})

	v.renderMessages()
	content := v.viewport.View()

	// Both messages should have sender headers (day change forces it)
	assert.Equal(t, 2, strings.Count(content, "Alice"), "both messages should show sender header across day boundary")
	// Both days should have date separators
	assert.Contains(t, content, "Jun 10")
	assert.Contains(t, content, "Jun 11")
}

func TestChat_UTCTimestamps_LocalDaySeparators(t *testing.T) {
	// API timestamps arrive in UTC. Day separators should follow local-day
	// boundaries, not UTC boundaries. Use two UTC timestamps that fall on
	// different UTC days but the same local day when local is UTC+5 or similar.
	// Since we can't control the test machine's timezone, we verify the simpler
	// invariant: two UTC timestamps on the same local day produce no separator,
	// while two on different local days do.
	now := time.Now()
	localNoon := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local)
	// Convert to UTC — the actual hour will differ, but the local day is the same
	utcNoon := localNoon.UTC()
	utcNoonPlus1 := utcNoon.Add(time.Hour)

	v := testChatWithLines([]workspace.ChatLineInfo{
		{ID: 1, Body: "first", Creator: "Alice", CreatedAt: "12:00pm", CreatedAtTS: utcNoon},
		{ID: 2, Body: "second", Creator: "Bob", CreatedAt: "1:00pm", CreatedAtTS: utcNoonPlus1},
	})

	v.renderMessages()
	content := v.viewport.View()

	// Both messages are on the same local day — should see exactly one date separator
	dateSepCount := strings.Count(content, "──")
	assert.Equal(t, 2, dateSepCount, "same local day should produce one date separator (2 dashes)")
}

func testChat() *Chat {
	styles := tui.NewStyles()
	comp := widget.NewComposer(styles, widget.WithMode(widget.ComposerQuick))
	pool := testChatPool()
	return &Chat{
		pool:     pool,
		styles:   styles,
		keys:     defaultChatKeyMap(),
		composer: comp,
		mode:     chatModeInput,
	}
}

func TestChat_FocusMsg_RefocusesComposer(t *testing.T) {
	v := testChat()
	v.mode = chatModeInput
	v.composer.Blur()

	_, cmd := v.Update(workspace.FocusMsg{})
	assert.NotNil(t, cmd, "FocusMsg should return composer focus cmd in input mode")
}

func TestChat_FocusMsg_ScrollModeDoesNotFocusComposer(t *testing.T) {
	v := testChat()
	v.mode = chatModeScroll
	v.composer.Blur()

	_, cmd := v.Update(workspace.FocusMsg{})
	assert.Nil(t, cmd, "FocusMsg should not return composer focus cmd in scroll mode")
}

func TestChat_ShortHelp_ModeAware(t *testing.T) {
	v := testChat()

	// Input mode: should show scroll escape, not compose
	v.mode = chatModeInput
	hints := v.ShortHelp()
	keys := helpKeys(hints)
	assert.Contains(t, keys, "esc", "input mode should show esc/scroll")
	assert.NotContains(t, keys, "i", "input mode should not show i/compose")

	// Scroll mode: should show compose, not scroll escape
	v.mode = chatModeScroll
	hints = v.ShortHelp()
	keys = helpKeys(hints)
	assert.Contains(t, keys, "i", "scroll mode should show i/compose")
	assert.NotContains(t, keys, "esc", "scroll mode should not show esc")
}

func helpKeys(bindings []key.Binding) []string {
	keys := make([]string, 0, len(bindings))
	for _, b := range bindings {
		keys = append(keys, b.Help().Key)
	}
	return keys
}

func testPollingChat() *Chat {
	styles := tui.NewStyles()
	pool := data.NewPool[data.ChatLinesResult](
		"chat:test",
		data.PoolConfig{FreshTTL: time.Hour, PollBase: 5 * time.Second},
		func(context.Context) (data.ChatLinesResult, error) {
			return data.ChatLinesResult{}, nil
		},
	)
	pool.Set(data.ChatLinesResult{})

	session := workspace.NewTestSessionWithHub()
	session.Hub().EnsureAccount("test-account")
	session.Hub().EnsureProject(99)

	return &Chat{
		session:  session,
		pool:     pool,
		styles:   styles,
		keys:     defaultChatKeyMap(),
		composer: widget.NewComposer(styles, widget.WithMode(widget.ComposerQuick)),
		mode:     chatModeInput,
	}
}

func TestChat_StalePollGen_Dropped(t *testing.T) {
	v := testPollingChat()
	poolKey := v.pool.Key()

	cmd := v.schedulePoll()
	require.NotNil(t, cmd)
	assert.Equal(t, uint64(1), v.pollGen)

	v.Update(workspace.TerminalFocusMsg{})
	assert.Equal(t, uint64(2), v.pollGen)

	_, cmd = v.Update(data.PollMsg{Tag: poolKey, Gen: 1})
	assert.Nil(t, cmd, "stale gen PollMsg should be dropped")

	_, cmd = v.Update(data.PollMsg{Tag: poolKey, Gen: 2})
	assert.NotNil(t, cmd, "current gen PollMsg should be processed")
}

func TestSameTimeGroup(t *testing.T) {
	now := time.Now()
	assert.True(t, sameTimeGroup(now, now.Add(4*time.Minute)), "within 5 min should group")
	assert.True(t, sameTimeGroup(now, now.Add(5*time.Minute)), "exactly 5 min should group")
	assert.False(t, sameTimeGroup(now, now.Add(6*time.Minute)), "over 5 min should not group")
	assert.False(t, sameTimeGroup(now, now.Add(-1*time.Minute)), "negative delta should not group")
	assert.False(t, sameTimeGroup(time.Time{}, now), "zero time should not group")
}
