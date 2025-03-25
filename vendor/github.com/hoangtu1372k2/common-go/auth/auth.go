package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

var isJwksCacheInitiallized bool = false
var ctx context.Context
var Cancel context.CancelFunc
var jwksCache *jwk.Cache

func ValidateToken(c *gin.Context, jwksURL string) bool {

	// Get public keys
	if !isJwksCacheInitiallized {
		ctx, Cancel = context.WithCancel(context.Background())
		// First, set up the `jwk.Cache` object. Pass it a `context.Context`
		// object to control the lifecycle of the background fetching goroutine.
		//
		// Note that by default refreshes only happen very 15 minutes at the
		// earliest. If need to control this, use `jwk.WithRefreshWindow()`
		jwksCache = jwk.NewCache(ctx)

		// Tell *jwk.Cache that we only want to refresh this JWKS
		// when it needs to (based on Cache-Control or Expires header from
		// the HTTP response). If the calculated minimum refresh interval is less
		// than 15 minutes, don't go refreshing any earlier than 15 minutes.
		jwksCache.Register(jwksURL, jwk.WithMinRefreshInterval(15*time.Minute))

		// Refresh the JWKS once before getting into the main loop.
		// This allows you to check if the JWKS is available before we start
		// a long-running program
		_, err := jwksCache.Refresh(ctx, jwksURL)
		if err != nil {
			fmt.Printf("failed to refresh google JWKS: %s\n", err)
			return false
		}
		isJwksCacheInitiallized = true
	}

	select {
	case <-ctx.Done():
		return false
	default:
	}

	//jwksCache.Refresh(ctx, jwksURL)
	publicKeySet, err := jwksCache.Get(ctx, jwksURL)
	if err != nil {
		fmt.Printf("failed to fetch iam JWKS: %s\n", err)
		return false
	}
	// The returned `keyset` will always be "reasonably" new. It is important that
	// you always call `ar.Fetch()` before using the `keyset` as this is where the refreshing occurs.
	//
	// By "reasonably" we mean that we cannot guarantee that the keys will be refreshed
	// immediately after it has been rotated in the remote source. But it should be close\
	// enough, and should you need to forcefully refresh the token using the `(jwk.Cache).Refresh()` method.
	//
	// If re-fetching the keyset fails, a cached version will be returned from the previous successful
	// fetch upon calling `(jwk.Cache).Fetch()`.

	authInfos := c.Request.Header["Authorization"]
	if len(authInfos) == 0 {
		return false
	}

	bearerToken := strings.Split(authInfos[0], " ")[1]

	//spew.Dump(publicKeySet)
	key, ret := publicKeySet.Key(0)
	if ret {
		tokenPayload, err := jwt.Parse([]byte(bearerToken), jwt.WithKey(jwa.RS256, key), jwt.WithVerify(true), jwt.WithValidate(true))
		if err != nil {
			fmt.Println("Failed to parse jwt.\nError: ", err.Error())
			return false
		}

		// Token is valid, let get value of user_name in payload
		//spew.Dump(tokenPayload)
		user_name, isUserNameExist := tokenPayload.Get("user_name")
		if isUserNameExist {
			valueString, ok := user_name.(string)
			if ok {
				// Write user_name into gin context for later usage
				c.Set("user_name", valueString)
			}
		}

		return true
	}

	// If publicKeySet contain more than 1 key set, should use option jwt.WithKeySet(publicKeySet) instead of jwt.WithKey(jwa.RS256, key)
	// then both of JWT and public key must contain same "kid" in header
	return false
}
func GetUserID(c *gin.Context) string {
	userName, exists := c.Get("user_name")
	if !exists {
		return ""
	}

	if userStr, ok := userName.(string); ok {
		return userStr
	}
	return ""
}
