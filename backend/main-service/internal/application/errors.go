package application

import "errors"

// ErrForbidden is returned when a caller asks for a resource owned by someone
// else. The HTTP layer maps it to 404 so the existence of others' resources is
// not leaked.
var ErrForbidden = errors.New("forbidden")

// ErrInvalidArgument marks request validation failures that should be returned
// to HTTP clients as 400, not as internal errors.
var ErrInvalidArgument = errors.New("invalid argument")

// ErrNotFound marks application-owned resources that are absent from a durable
// edge store such as Valkey.
var ErrNotFound = errors.New("not found")
