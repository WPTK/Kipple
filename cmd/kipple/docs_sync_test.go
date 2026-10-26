package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/config"
)

// settingsPhrase finds "Settings, <Tab>[, <section>]" and "Settings > <tab>[ > <section>]". A name ends at a comma,
// a full stop, a semicolon, a bracket, a backtick, an arrow or the end of the line. "Zero Trust > Settings > Custom
// Pages" is Cloudflare's menu, not Kipple's, so a Settings that follows "Trust >" is skipped.
var settingsPhrase = regexp.MustCompile("(Trust > )?\\bSettings(?:, | > )([A-Z][^,.;:()`>\\n]*)(?:(?:, | > )([^,.;:()`>\\n]+))?")

// settingsDocs are the docs a person follows in the app. web/README.md and web/ACCESSIBILITY.md are left out: they
// describe the code, and they live in the web/ slot.
var settingsDocs = func() []string {
	var out []string
	for _, n := range userDocs {
		if !strings.HasPrefix(n, "web/") {
			out = append(out, n)
		}
	}
	return out
}()

// settingsTabs reads the Settings tab labels from SETTINGS_GROUPS, the one list the screen is built from.
func settingsTabs(t *testing.T) []string {
	src := readRepoFile(t, "web/src/screens/SettingsScreen.tsx")
	i := strings.Index(src, "export const SETTINGS_GROUPS")
	require.GreaterOrEqual(t, i, 0, "SETTINGS_GROUPS not found")
	end := strings.Index(src[i:], "] as const")
	require.Greater(t, end, 0)
	var tabs []string
	for _, m := range regexp.MustCompile(`label: "([^"]+)"`).FindAllStringSubmatch(src[i:i+end], -1) {
		tabs = append(tabs, m[1])
	}
	require.NotEmpty(t, tabs)
	return tabs
}

// uiText is every screen's source plus the setting labels, lower-cased: a section a doc names must be spelled
// somewhere in them.
func uiText(t *testing.T) string {
	var sb strings.Builder
	root := filepath.Join("..", "..", "web", "src")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".tsx") || strings.Contains(p, ".test.") {
			return err
		}
		b, err := os.ReadFile(p)
		sb.Write(b)
		return err
	})
	require.NoError(t, err)
	sb.WriteString(readRepoFile(t, "internal/api/settingsmeta.go"))
	return strings.ToLower(sb.String())
}

// foundPrefix reports whether the text, or its longest leading run of words, appears in the UI text. A doc may run on
// ("Export backup on the old server"), so words are dropped from the end until something matches; the match has to be
// at least five characters, so a lone "Set" cannot vouch for a section.
func foundPrefix(ui, text string) bool {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(text)))
	for n := len(words); n > 0; n-- {
		s := strings.Join(words[:n], " ")
		if len(s) >= 5 && strings.Contains(ui, s) {
			return true
		}
	}
	return false
}

// Docs name Settings tabs and sections by hand. One tab was wrong in three places before anything noticed, so a
// rename in the app now fails here until the docs follow.
func TestSettingsPhrasesExistInTheApp(t *testing.T) {
	tabs := settingsTabs(t)
	ui := uiText(t)
	for _, name := range settingsDocs {
		for _, m := range settingsPhrase.FindAllStringSubmatch(readRepoFile(t, name), -1) {
			if m[1] != "" {
				continue
			}
			tab, section := strings.TrimSpace(m[2]), strings.TrimSpace(m[3])
			matched := ""
			for _, label := range tabs {
				if tab == label || strings.HasPrefix(tab, label+" ") {
					matched = label
				}
			}
			if matched == "" {
				t.Errorf("%s: %q is not a Settings tab (tabs: %s)", name, m[0], strings.Join(tabs, ", "))
				continue
			}
			if section != "" && tab == matched && !foundPrefix(ui, section) {
				t.Errorf("%s: %q: no section or setting called %q in the app", name, m[0], section)
			}
		}
	}
}

func TestSettingsPhraseCatchesAWrongTab(t *testing.T) {
	m := settingsPhrase.FindStringSubmatch("in Settings, Account & Devices, Address and access; the")
	require.NotNil(t, m)
	require.Equal(t, "Account & Devices", strings.TrimSpace(m[2]))
	require.Equal(t, "Address and access", strings.TrimSpace(m[3]))
	require.NotEmpty(t, settingsPhrase.FindStringSubmatch("Zero Trust > Settings > Custom Pages")[1])
	require.False(t, foundPrefix("address and access", "Something else"))
	require.True(t, foundPrefix("export backup…", "Export backup on the old server"))
}

// A release candidate or a pre-release number in a user doc is a snapshot of a day that has passed; the doc should
// describe Kipple as it is. A version with a major above the newest release is still ahead and may be named.
var preRelease = regexp.MustCompile(`\b(\d+)\.\d+\.\d+-(?:alpha|beta|rc)\.\d+\b|(?i:release candidate)`)

func TestUserDocsNameNoPreReleaseOrCandidate(t *testing.T) {
	major := releasedMajor(t)
	for _, name := range userDocs {
		text := readRepoFile(t, name)
		for _, loc := range preRelease.FindAllStringSubmatchIndex(text, -1) {
			if loc[2] >= 0 {
				if m, _ := strconv.Atoi(text[loc[2]:loc[3]]); m > major {
					continue
				}
			}
			t.Errorf("%s: %q is history or a stale status; describe Kipple as it is", name, text[loc[0]:loc[1]])
		}
	}
}

func TestPreReleasePatternCatchesTheStaleForms(t *testing.T) {
	for _, in := range []string{"1.0.0-rc.1", "0.9.0-beta.1", "0.3.0-alpha.4", "a release candidate", "Release Candidate"} {
		require.NotNil(t, preRelease.FindStringIndex(in), in)
	}
	for _, in := range []string{"1.5.1", "v1.0.0", "SemVer with -rc.N"} {
		require.Nil(t, preRelease.FindStringIndex(in), in)
	}
}

// The defaults .env.example and deploy.md print are the ones config.Load applies. They were checked by eye.
func TestDocumentedEnvDefaultsMatchTheLoader(t *testing.T) {
	for _, k := range []string{"KIPPLE_ADDR", "KIPPLE_DATA", "KIPPLE_SCHED_TICK", "KIPPLE_FETCH_WORKERS", "KIPPLE_FETCH_PER_HOST", "KIPPLE_LOG_LEVEL"} {
		t.Setenv(k, "")
	}
	cfg, err := config.Load()
	require.NoError(t, err)
	defaults := map[string]string{
		"KIPPLE_ADDR":           cfg.Addr,
		"KIPPLE_DATA":           cfg.DataDir,
		"KIPPLE_SCHED_TICK":     cfg.SchedTick.String(),
		"KIPPLE_FETCH_WORKERS":  strconv.Itoa(cfg.FetchWorkers),
		"KIPPLE_FETCH_PER_HOST": strconv.Itoa(cfg.FetchPerHost),
		"KIPPLE_LOG_LEVEL":      strings.ToLower(cfg.LogLevel.String()),
	}
	env := readRepoFile(t, ".env.example")
	deploy := readRepoFile(t, "docs/deploy.md")
	unquote := strings.NewReplacer("`", "", `"`, "")
	for name, want := range defaults {
		// The commented example line carries the default.
		line := regexp.MustCompile(`(?m)^# ` + name + `=(.*)$`).FindStringSubmatch(env)
		require.NotNil(t, line, "%s has no example line in .env.example", name)
		require.Equal(t, want, line[1], "%s in .env.example", name)
		// So does a sentence "Default X" in the comment block above it.
		block := env[:strings.Index(env, line[0])]
		block = block[strings.LastIndex(block, "\n\n")+2:]
		if d := regexp.MustCompile(`(?i)\bdefault ([^\s.,;)]+)`).FindStringSubmatch(block); d != nil {
			require.Equal(t, want, unquote.Replace(d[1]), "the default %s is said to have in .env.example", name)
		}
		// And the deploy guide's table row, where it gives one.
		row := regexp.MustCompile(`(?m)^\| ` + "`" + name + "`" + ` \|[^\n]*?default ` + "`([^`]+)`").FindStringSubmatch(deploy)
		if row != nil {
			require.Equal(t, want, row[1], "the default %s is said to have in docs/deploy.md", name)
		}
	}
}
