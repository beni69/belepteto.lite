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
	logger           *slog.Logger
	tmpl             *Templates
	store            *Store
	access           *AccessService
	externalAPIToken string
	sso              *oidcAuthenticator
	build            buildView
}

func newRouter(logger *slog.Logger, tmpl *Templates, store *Store, access *AccessService, externalAPIToken string, sso *oidcAuthenticator) http.Handler {
	s := &server{
		logger:           logger,
		tmpl:             tmpl,
		store:            store,
		access:           access,
		externalAPIToken: externalAPIToken,
		sso:              sso,
		build:            buildViewFor(currentBuildIdentity()),
	}

	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(logger))

	r.Get("/healthz", handleHealthz)
	r.Get("/manifest.webmanifest", handleManifest)

	// Machine-to-machine API. Every endpoint in this group requires the
	// configured external bearer token.
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.RequireExternalAPIToken)
		r.Put("/access-grant-sets", s.handleExternalAccessGrantSetPut)
		r.Get("/access-grant-sets", s.handleExternalAccessGrantSetGet)
	})

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
		r.Get("/htmx/door-open-control", s.handleDoorOpenControl)
		r.Post("/htmx/door/open", s.handleDoorOpen)

		r.Get("/htmx/cards", s.handleCardList)
		r.Post("/htmx/cards/pair", s.handleCardPairStart)
		r.Get("/htmx/cards/pair/status", s.handleCardPairStatus)
		r.Delete("/htmx/cards/{id}", s.handleCardDelete)
		r.Post("/htmx/cards/{id}/relabel", s.handleCardRelabel)
		r.Post("/htmx/account/password", s.handlePasswordChange)

		r.Group(func(r chi.Router) {
			r.Use(s.RequireAdmin)
			r.Get("/admin", s.handleAdmin)

			r.Get("/htmx/admin/users", s.handleAdminUserList)
			r.Post("/htmx/admin/users", s.handleAdminUserCreate)
			r.Get("/htmx/admin/access-grant-users", s.handleAdminAccessGrantUsers)
			r.Post("/htmx/admin/users/{id}/activate", s.handleAdminUserActivate)
			r.Post("/htmx/admin/users/{id}/deactivate", s.handleAdminUserDeactivate)
			r.Post("/htmx/admin/users/{id}/pair", s.handleAdminUserPairStart)
			r.Get("/htmx/admin/users/{id}/pair/status", s.handleAdminUserPairStatus)
			r.Get("/htmx/admin/users/{userID}/cards", s.handleAdminUserCards)
			r.Post("/htmx/admin/users/{userID}/cards/{cardID}/relabel", s.handleAdminCardRelabel)
			r.Delete("/htmx/admin/users/{userID}/cards/{cardID}", s.handleAdminCardDelete)
			r.Post("/htmx/admin/users/{id}/scheduled-access", s.handleAdminUserScheduledAccess)
			r.Post("/htmx/admin/users/{id}/permanent-access", s.handleAdminUserPermanentAccess)

			r.Get("/htmx/admin/access-grants", s.handleAdminAccessGrantList)
			r.Post("/htmx/admin/access-grants", s.handleAdminAccessGrantCreate)
			r.Post("/htmx/admin/access-grants/{id}/revoke", s.handleAdminAccessGrantRevoke)

			r.Get("/htmx/admin/logs/entries", s.handleAdminEntryLog)
			r.Get("/htmx/admin/logs/cardreads", s.handleAdminCardReadLog)
		})
	})

	return r
}

func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	canOpenDoor := s.canOpenDoorForPage(u)
	s.render(w, "home", map[string]any{
		"IsAdmin":     u.Admin,
		"Account":     accountViewForUser(u),
		"CanOpenDoor": canOpenDoor,
	})
}

func (s *server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	users, err := s.store.ListUsers()
	if err != nil {
		s.logger.Error("list users for admin page", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "admin", map[string]any{
		"IsAdmin":     u.Admin,
		"CanOpenDoor": s.canOpenDoorForPage(u),
		"Users":       users,
	})
}

func (s *server) canOpenDoorForPage(user *User) bool {
	allowed, err := s.access.CanOpenDoor(user.ID)
	if err != nil {
		s.logger.Error("check door access for page", "user", user.ID, "err", err)
		return false
	}
	return allowed
}

func (s *server) render(w http.ResponseWriter, name string, data map[string]any) {
	data["Build"] = s.build
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

func handleManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	http.ServeFileFS(w, r, frontend.FS, "static/manifest.webmanifest")
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
