package core_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Unscoped skips the tenant rule (keel's events.DB), so Promptdam calls it
// only where a job must look across projects, each file here for its
// reason; a new caller is added here on purpose, never by accident.
// Tests may.
func TestUnscopedOnlyWhereAllowed(t *testing.T) {
	allowed := map[string]string{
		"features/activity/activity.go":       "async inserts, the rows carrying their project",
		"features/alerts/evaluate.go":         "the projects with enabled rules, grouped by project",
		"features/cachepolicy/cachepolicy.go": "the projects due a lifetime update",
		"features/training/training.go":       "each project's routers' latest evaluations",
		"features/firewall/watch.go":          "blocked calls and weekly digests, grouped by project",
		"features/cacheprofile/learn.go":      "the pooled caching profiles: counts per model, never a project's",
	}
	root := ".."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "node_modules" || name == "ent" || strings.HasPrefix(name, ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, ok := allowed[rel]; !ok && strings.Contains(string(b), ".Unscoped()") {
			t.Errorf("%s calls events.DB.Unscoped: read with Select/Query and a tenant, or add it here with its reason", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range allowed {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s is allowed but gone: take it off the list", rel)
		}
	}
}
