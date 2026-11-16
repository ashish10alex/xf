package warehouse

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrAuth means there are no usable credentials: none are set up, the login
// has expired, or the warehouse rejected them. Logging in again usually
// fixes it. Its words are BigQuery's, the only warehouse so far.
var ErrAuth = errors.New("no valid Google credentials")

// AuthHint tells the user how to fix ErrAuth.
const AuthHint = "run `gcloud auth application-default login`"

// ErrNoWarehouse means a warehouse that runs queries on compute the user
// picks (a Databricks SQL warehouse) has none picked yet.
var ErrNoWarehouse = errors.New("no SQL warehouse chosen")

// APIError is an error response from a warehouse's API other than one
// ErrAuth stands for.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status) }

// IsNotFound says err is the warehouse's 404.
func IsNotFound(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusNotFound
}

// IsForbidden says err is the warehouse's 403.
func IsForbidden(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusForbidden
}
