package admin

import (
	aphttp "gateway/http"
	apsql "gateway/sql"
	"net/http"
)

//go:generate ./controller.rb --model Account
//go:generate ./controller.rb --model API --account
//go:generate ./controller.rb --model EndpointGroup --account --api
//go:generate ./controller.rb --model Environment --account --api --check-delete
//go:generate ./controller.rb --model Host --account --api
//go:generate ./controller.rb --model Library --account --api
//go:generate ./controller.rb --model ProxyEndpoint --account --api
//go:generate ./controller.rb --model RemoteEndpoint --account --api --check-delete
//go:generate ./controller.rb --model User --account --custom-struct --transform-method c.sanitize --transform-type sanitizedUser

// ResourceController defines what we expect a controller to do to route
// a RESTful resource
type ResourceController interface {
	List(w http.ResponseWriter, r *http.Request, db *apsql.DB) aphttp.Error
	Create(w http.ResponseWriter, r *http.Request, tx *apsql.Tx) aphttp.Error
	Show(w http.ResponseWriter, r *http.Request, db *apsql.DB) aphttp.Error
	Update(w http.ResponseWriter, r *http.Request, tx *apsql.Tx) aphttp.Error
	Delete(w http.ResponseWriter, r *http.Request, tx *apsql.Tx) aphttp.Error
}

type BaseController struct{}

func (c *BaseController) accountID(r *http.Request) int64 {
	return accountIDFromSession(r)
}

func (c *BaseController) apiID(r *http.Request) int64 {
	return apiIDFromPath(r)
}
