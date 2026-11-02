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
	access *AccessService
	sso    *oidcAuthenticator
}

func newRouter(logger *slog.Logger, tmpl *Templates, store *Store, access *AccessService, sso ...*oidcAuthenticator) http.Handler {
	var oidcAuth *oidcAuthenticator
	if len(sso) > 0 {
		oidcAuth = sso[0]
	}
	s := &server{logger: logger, tmpl: tmpl, store: store, access: access, sso: oidcAuth}

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
	if s.sso != nil {
		r.Get("/sso/login", s.handleOIDCLogin)
		r.Get(s.sso.callbackPath, s.handleOIDCCallback)
	}

	// Authenticated pages.
	r.Group(func(r chi.Router) {
		r.Use(s.RequireAuth)
		r.Get("/", s.handleHome)

		r.Get("/htmx/cards", s.handleCardList)
		r.Post("/htmx/cards/pair", s.handleCardPairStart)
		r.Get("/htmx/cards/pair/status", s.handleCardPairStatus)
		r.Delete("/htmx/cards/{id}", s.handleCardDelete)
		r.Post("/htmx/cards/{id}/relabel", s.handleCardRelabel)

		r.Group(func(r chi.Router) {
			r.Use(s.RequireAdmin)
			r.Get("/admin", s.handleAdmin)

			r.Get("/htmx/admin/users", s.handleAdminUserList)
			r.Post("/htmx/admin/users", s.handleAdminUserCreate)
			r.Post("/htmx/admin/users/{id}/activate", s.handleAdminUserActivate)
			r.Post("/htmx/admin/users/{id}/deactivate", s.handleAdminUserDeactivate)

			r.Get("/htmx/admin/logs/entries", s.handleAdminEntryLog)
			r.Get("/htmx/admin/logs/cardreads", s.handleAdminCardReadLog)
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
