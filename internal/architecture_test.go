package internal_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestDependencyRule enforces docs/ARCHITECTURE.md §1: imports point inward
// only. It reads the imports of every non-test file under internal/, so a
// boundary violation fails the build instead of waiting for a review.
//
// This is the same guard the Kiosk Agent uses, so both repositories are held to
// one rulebook.
func TestDependencyRule(t *testing.T) {
	const mod = "vending/internal/"
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(filepath.Dir(path))
		layer := strings.Split(pkg, "/")[0]
		for _, spec := range f.Imports {
			imp, _ := strconv.Unquote(spec.Path.Value)
			if layer == "usecase" && imp == "net/http" {
				t.Errorf("%s: a usecase must not import net/http (§4)", path)
			}
			if layer == "usecase" && imp == "database/sql" {
				t.Errorf("%s: a usecase must not import database/sql (§4): the port hides the driver", path)
			}
			if !strings.HasPrefix(imp, mod) {
				continue
			}
			target := strings.TrimPrefix(imp, mod)
			if why := forbidden(layer, target); why != "" {
				t.Errorf("%s imports %s: %s", path, target, why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// legacy names the rearchitecture is dismantling (docs/REARCHITECTURE.md).
// Nothing in a finished layer may import them, or the migration would gain a
// new dependency on the very package it is emptying.
var legacy = map[string]bool{"core": true, "httpapi": true, "seed": true}

func forbidden(layer, target string) string {
	parts := strings.Split(target, "/")
	to := parts[0]
	if legacy[to] {
		switch layer {
		case "model", "common", "repository", "usecase", "delivery":
			return "it is being dismantled: take what you need from model, or finish the slice that owns it"
		}
	}
	switch layer {
	case "model":
		return "model imports nothing internal"
	case "common":
		if to != "model" && to != "common" {
			return "common may import only model"
		}
	case "repository":
		if to == "usecase" || to == "delivery" {
			return "a repository must not import usecase or delivery"
		}
	case "usecase":
		if to == "delivery" {
			return "a usecase must not import delivery"
		}
		if to == "repository" && len(parts) > 2 {
			return "a usecase imports the repository port, never an adapter (§2)"
		}
	case "delivery":
		if to == "repository" {
			return "delivery must not import a repository"
		}
		if to == "usecase" && len(parts) > 2 {
			return "delivery imports the usecase port only"
		}
	}
	return ""
}
