package middleware

import "testing"

func TestRouteKey(t *testing.T) {
	key, err := routeKey("/api/v1/render/testing")
	if err != nil {
		t.Fatalf("routeKey returned unexpected error: %v", err)
	}
	if key != "render.testing" {
		t.Fatalf("expected render.testing, got %q", key)
	}
}

func TestGetAuthDatabaseNameDefaultsToAuth(t *testing.T) {
	t.Setenv("AUTH_DATABASE", "")
	if got := getAuthDatabaseName(); got != defaultAuthDB {
		t.Fatalf("expected %q default auth db, got %q", defaultAuthDB, got)
	}
}
