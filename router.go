package main

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"git.sch.bme.hu/vben/belepteto.lite/frontend"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type server struct {
	logger *slog.Logger
	tmpl   *Templates
	store  *Store
}

func newRouter(logger *slog.Logger, tmpl *Templates, store *Store) http.Handler {
	s := &server{logger: logger, tmpl: tmpl, store: store}

	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(logger))

	r.Get("/healthz", handleHealthz)

	staticFS, _ := fs.Sub(frontend.FS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Public auth routes.
	r.Get("/login", s.handleLoginPage)
	r.Post("/login", s.handleLoginSubmit)
	r.Post("/logout", s.handleLogout)

	// Authenticated pages.
	r.Group(func(r chi.Router) {
		r.Use(s.RequireAuth)
		r.Get("/", s.handleHome)

		r.Group(func(r chi.Router) {
			r.Use(s.RequireAdmin)
			r.Get("/admin", s.handleAdmin)
		})
	})

	return r
}

func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	s.render(w, "home", map[string]any{"IsAdmin": u.Admin})
}

func (s *server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	s.render(w, "admin", map[string]any{"IsAdmin": u.Admin})
}

func (s *server) render(w http.ResponseWriter, name string, data any) {
	if err := s.tmpl.Page(w, name, data); err != nil {
		s.logger.Error("render page", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			next.ServeHTTP(ww, r)

			logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration", time.Since(start).String(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
