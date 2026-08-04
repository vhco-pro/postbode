package webui

import "errors"

// errEmptyToken guards NewServer against being constructed with no session
// token at all — F-46 requires every mutating request to carry a valid
// token, which is meaningless if the configured token is empty.
var errEmptyToken = errors.New("webui: NewServer: token must not be empty")
