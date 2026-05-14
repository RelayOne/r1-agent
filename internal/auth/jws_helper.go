package auth

// jws_helper.go — tiny wrapper around jws.ParseString. Kept in its own
// file so the package-level imports stay clean.

import (
	"github.com/lestrrat-go/jwx/v2/jws"
)

func jwsParse(token string) (*jws.Message, error) {
	return jws.Parse([]byte(token))
}
