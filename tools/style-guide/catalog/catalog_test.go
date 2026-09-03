package catalog

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestNewDefaultsDark(t *testing.T) {
	if !New().isDark {
		t.Fatal("New should default to dark")
	}
}

func TestKeyToggleTheme(t *testing.T) {
	m := New()
	m.width, m.height = 80, 24
	before := m.isDark
	m.key("t")
	if m.isDark == before || m.theme.IsDark != m.isDark {
		t.Fatal("t should toggle the complete theme")
	}
}

// TestViewPaintsThemeBackgroundAndFollowsToggle keeps the catalog honest about
// the surface it previews on: it has to paint the same background the app pins,
// and the t toggle has to carry that background with the rest of the theme.
// Without this the swatches are judged against the author's own terminal.
func TestViewPaintsThemeBackgroundAndFollowsToggle(t *testing.T) {
	m := New()
	m.width, m.height = 80, 24

	dark := m.View().BackgroundColor
	if dark == nil {
		t.Fatal("view background unset; swatches would render on the terminal's own background")
	}
	if dark != m.theme.Background {
		t.Errorf("view background = %v, want theme background %v", dark, m.theme.Background)
	}

	m.key("t")
	light := m.View().BackgroundColor
	if light == nil {
		t.Fatal("view background unset after toggling to light")
	}
	if light == dark {
		t.Errorf("background stayed %v across the t toggle", dark)
	}
	if light != m.theme.Background {
		t.Errorf("view background = %v, want theme background %v", light, m.theme.Background)
	}
}

func TestKeyNavAdvancesGroupAndResetsOffset(t *testing.T) {
	m := New()
	m.width, m.height = 80, 24
	m.offset = 5
	m.key("tab")
	if m.group != groupSemanticRoles {
		t.Fatalf("tab should advance group, got %v", m.group)
	}
	if m.offset != 0 {
		t.Fatal("group change should reset offset")
	}
	m.key("tab")
	if m.group != groupSharedComponents {
		t.Fatalf("second tab should open shared components, got %v", m.group)
	}
}

func TestKeyQuit(t *testing.T) {
	cmd := New().key("q")
	if cmd == nil {
		t.Fatal("q should return a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q should quit")
	}
}

func TestWindowSizeUpdatesDimensions(t *testing.T) {
	m := New()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.width != 100 || m.height != 40 {
		t.Fatal("resize not applied")
	}
}

func TestScrollClampsToZeroAtTop(t *testing.T) {
	m := New()
	m.width, m.height = 20, 5
	m.scroll(1000)
	m.scroll(-1000)
	if m.offset != 0 {
		t.Fatalf("offset = %d, want 0", m.offset)
	}
}

func TestViewIncludesFooter(t *testing.T) {
	m := New()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	v := m.View()
	if !strings.Contains(v.Content, "DARK") {
		t.Fatal("view missing footer mode")
	}
	if !strings.Contains(v.Content, fmt.Sprintf("/ %d", numGroups)) {
		t.Fatal("view missing footer position")
	}
}
