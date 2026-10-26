package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrMissingAuthHeader = errors.New("missing Authorization header")
	ErrInvalidToken      = errors.New("invalid or expired token")
	ErrForbidden         = errors.New("forbidden: insufficient permissions")
)

type contextKey string

const (
	ProviderIDContextKey contextKey = "providerId"
	RolesContextKey      contextKey = "roles"
)

type JWTValidator struct {
	secretKey []byte
	issuer    string
	mu        sync.RWMutex
	rsaKeys   map[string]*rsa.PublicKey
	httpCli   *http.Client
}

func NewJWTValidator(secret string, issuer string) *JWTValidator {
	return &JWTValidator{
		secretKey: []byte(secret),
		issuer:    strings.TrimRight(issuer, "/"),
		rsaKeys:   make(map[string]*rsa.PublicKey),
		httpCli:   &http.Client{Timeout: 5 * time.Second},
	}
}

// CustomClaims represents Keycloak/OAuth2 token claims
type CustomClaims struct {
	ProviderID    string   `json:"providerId"`
	ClientID      string   `json:"clientId"`
	ClientIDSnake string   `json:"client_id"`
	AZP           string   `json:"azp"`
	Roles         []string `json:"roles"`
	RealmAccess   struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	jwt.RegisteredClaims
}

func (v *JWTValidator) ValidateToken(tokenStr string) (*CustomClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &CustomClaims{}, func(t *jwt.Token) (interface{}, error) {
		switch t.Method.(type) {
		case *jwt.SigningMethodHMAC:
			return v.secretKey, nil
		case *jwt.SigningMethodRSA:
			kid, _ := t.Header["kid"].(string)
			return v.getRSAPublicKey(kid)
		default:
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
	})
	if err != nil {
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*CustomClaims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	// Merge realm_access.roles from Keycloak if present
	if len(claims.RealmAccess.Roles) > 0 {
		claims.Roles = append(claims.Roles, claims.RealmAccess.Roles...)
	}

	// Fallback chain for providerId: providerId -> clientId -> client_id -> azp
	if claims.ProviderID == "" {
		switch {
		case claims.ClientID != "":
			claims.ProviderID = claims.ClientID
		case claims.ClientIDSnake != "":
			claims.ProviderID = claims.ClientIDSnake
		case claims.AZP != "":
			claims.ProviderID = claims.AZP
		}
	}

	return claims, nil
}

func (v *JWTValidator) getRSAPublicKey(kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	if key, ok := v.rsaKeys[kid]; ok {
		v.mu.RUnlock()
		return key, nil
	}
	v.mu.RUnlock()

	if v.issuer == "" {
		return nil, ErrInvalidToken
	}

	jwksURL := v.issuer + "/protocol/openid-connect/certs"
	resp, err := v.httpCli.Get(jwksURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		eInt := 0
		for _, b := range eBytes {
			eInt = (eInt << 8) + int(b)
		}
		pubKey := &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: eInt,
		}
		v.rsaKeys[k.Kid] = pubKey
	}

	if key, ok := v.rsaKeys[kid]; ok {
		return key, nil
	}
	return nil, ErrInvalidToken
}

// Middleware creates an HTTP middleware that validates the JWT and injects claims into context.
func (v *JWTValidator) Middleware(requiredRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"missing authorization header"}`))
				return
			}

			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid authorization format, expected Bearer <token>"}`))
				return
			}

			claims, err := v.ValidateToken(parts[1])
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid or expired token"}`))
				return
			}

			if requiredRole != "" {
				hasRole := false
				for _, role := range claims.Roles {
					if role == requiredRole || role == "admin" {
						hasRole = true
						break
					}
				}
				if !hasRole {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":"forbidden: insufficient role"}`))
					return
				}
			}

			ctx := context.WithValue(r.Context(), ProviderIDContextKey, claims.ProviderID)
			ctx = context.WithValue(ctx, RolesContextKey, claims.Roles)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetProviderIDFromContext(ctx context.Context) string {
	val, ok := ctx.Value(ProviderIDContextKey).(string)
	if !ok {
		return ""
	}
	return val
}

func HasInternalRole(ctx context.Context) bool {
	roles, ok := ctx.Value(RolesContextKey).([]string)
	if !ok {
		return false
	}
	for _, r := range roles {
		if r == "internal" || r == "admin" {
			return true
		}
	}
	return false
}
