package main

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"

	"git.sch.bme.hu/vben/belepteto.lite/frontend"
)

type Templates struct {
	pages     map[string]*template.Template
	fragments *template.Template
}

func LoadTemplates() (*Templates, error) {
	const dir = "templates"

	// Each full page is base.html + that page's content template, parsed together
	// so the page's {{define "content"}} / {{define "title"}} satisfy base's slots.
	pages := map[string]string{
		"home":  dir + "/home.html",
		"admin": dir + "/admin.html",
	}

	t := &Templates{pages: make(map[string]*template.Template)}

	for name, file := range pages {
		tmpl, err := template.New(name).ParseFS(frontend.FS, dir+"/base.html", dir+"/partials/door-open-control.html", file)
		if err != nil {
			return nil, fmt.Errorf("parse page %q: %w", name, err)
		}
		t.pages[name] = tmpl
	}

	// login is a standalone page (no nav/base layout).
	login, err := template.New("login").ParseFS(frontend.FS, dir+"/login.html")
	if err != nil {
		return nil, fmt.Errorf("parse login: %w", err)
	}
	t.pages["login"] = login

	// Fragments for /htmx/* responses, all parsed into one set and executed by
	// their defined name.
	frags, err := template.New("fragments").ParseFS(frontend.FS, dir+"/partials/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse fragments: %w", err)
	}
	t.fragments = frags

	return t, nil
}

// Page renders a full page. login renders its own root template; the others
// render through the shared base layout.
func (t *Templates) Page(w http.ResponseWriter, name string, data any) error {
	tmpl, ok := t.pages[name]
	if !ok {
		return fmt.Errorf("unknown page %q", name)
	}

	root := "base"
	if name == "login" {
		root = "login"
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, root, data); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}

// Fragment renders a single named fragment for an HTMX swap.
func (t *Templates) Fragment(w http.ResponseWriter, name string, data any) error {
	var buf bytes.Buffer
	if err := t.fragments.ExecuteTemplate(&buf, name, data); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}
