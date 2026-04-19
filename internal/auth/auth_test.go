package auth_test

import (
	"github.com/oklahomer/blabby/internal/auth"
)

// Compile-time interface satisfaction checks.
var _ auth.Authenticator = (*auth.JWTAuthenticator)(nil)
var _ auth.UserStore = (*auth.InMemoryUserStore)(nil)
