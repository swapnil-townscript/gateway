package admin

import (
	"fmt"
	"net/http"
	"strings"

	"gateway/config"
	"gateway/core"
	aphttp "gateway/http"
	"gateway/logreport"
	//"gateway/model"
	sql "gateway/sql"
	"gateway/store"

	"github.com/gorilla/mux"
)

var (
	defaultDomain string
)

// Setup sets up the session and adds admin routes.
func Setup(router *mux.Router, db *sql.DB, s store.Store, configuration config.Configuration, c *core.Core) {
	conf, psconf := configuration.Admin, configuration.Proxy
	var admin aphttp.Router
	admin = aphttp.NewAccessLoggingRouter(config.Admin, conf.RequestIDHeader,
		subrouter(router, conf))

	if conf.CORSEnabled {
		admin = aphttp.NewCORSAwareRouter(conf.CORSOrigin, admin)
	}

	if !conf.DevMode {
		setupSessions(conf)

		// siteAdmin is additionally protected for the site owner
		siteAdmin := aphttp.NewHTTPBasicRouter(conf.Username, conf.Password, conf.Realm, admin)
		RouteResource(&AccountsController{}, "/accounts", siteAdmin, db, conf)
		RouteResource(&UsersController{BaseController{accountID: accountIDFromPath, userID: userIDDummy,
			auth: aphttp.AuthTypeSite}}, "/accounts/{accountID}/users", siteAdmin, db, conf)

		// sessions are unprotected to allow users to authenticate
		RouteSessions("/sessions", admin, db, conf)
	}

	defaultDomain = psconf.Domain

	// protected by requiring login (except dev mode)
	accountID := accountIDFromSession
	userID := userIDFromSession
	authAdmin := NewSessionAuthRouter(admin, []string{"OPTIONS"}, false)
	authAdminUser := NewSessionAuthRouter(admin, []string{"OPTIONS"}, true)
	if conf.DevMode {
		accountID = accountIDForDevMode(db)
		userID = userIDForDevMode(db)
		authAdmin = admin
		authAdminUser = admin
	}

	base := BaseController{conf: conf, accountID: accountID, userID: userID,
		SMTP: configuration.SMTP, ProxyServer: psconf}

	RouteNotify(&NotifyController{BaseController: base}, "/notifications", authAdmin, db, s)

	if conf.EnableBroker {
		broker, err := newAggregator(conf)
		if err != nil {
			logreport.Fatal(err)
		}
		stream := &LogStreamController{base, broker}
		RouteLogStream(stream, "/logs/socket", authAdmin)
		RouteLogStream(stream, "/apis/{apiID}/logs/socket", authAdmin)
		RouteLogStream(stream, "/apis/{apiID}/proxy_endpoints/{endpointID}/logs/socket", authAdmin)
	}

	search := &LogSearchController{configuration.Elastic, base}
	RouteLogSearch(search, "/logs", authAdmin, db, conf)
	RouteLogSearch(search, "/apis/{apiID}/logs", authAdmin, db, conf)
	RouteLogSearch(search, "/apis/{apiID}/proxy_endpoints/{endpointID}/logs", authAdmin, db, conf)

	RouteResource(&UsersController{base}, "/users", authAdminUser, db, conf)
	if conf.EnableRegistration {
		RouteRegistration(&RegistrationController{base}, "/registrations", admin, db, conf)
		RouteConfirmation(&ConfirmationController{base}, "/confirmation", admin, db, conf)
	}
	RoutePasswordReset(&PasswordResetController{base}, "/password_reset", admin, db, conf)
	RoutePasswordResetCheck(&PasswordResetCheckController{base}, "/password_reset_check", admin, db, conf)
	RoutePasswordResetConfirmation(&PasswordResetConfirmationController{base}, "/password_reset_confirmation", admin, db, conf)

	apisController := &APIsController{base}
	RouteAPIExport(apisController, "/apis/{id}/export", authAdmin, db, conf)
	RouteResource(apisController, "/apis", authAdmin, db, conf)

	testController := &TestController{base, psconf}
	RouteTest(testController, "/apis/{apiID}/proxy_endpoints/{endpointID}/tests/{testID}/test", authAdmin, db, conf)

	RouteResource(&HostsController{base}, "/apis/{apiID}/hosts", authAdmin, db, conf)
	RouteResource(&EnvironmentsController{base}, "/apis/{apiID}/environments", authAdmin, db, conf)
	RouteResource(&LibrariesController{base}, "/apis/{apiID}/libraries", authAdmin, db, conf)
	RouteResource(&EndpointGroupsController{base}, "/apis/{apiID}/endpoint_groups", authAdmin, db, conf)
	RouteResource(&RemoteEndpointsController{base}, "/apis/{apiID}/remote_endpoints", authAdmin, db, conf)
	RouteResource(&ProxyEndpointsController{base}, "/apis/{apiID}/proxy_endpoints", authAdmin, db, conf)
	RouteResource(&ProxyEndpointSchemasController{base}, "/apis/{apiID}/proxy_endpoints/{endpointID}/schemas", authAdmin, db, conf)
	scratchPadController := &MetaScratchPadsController{ScratchPadsController{base}, c}
	RouteScratchPads(scratchPadController, "/apis/{apiID}/remote_endpoints/{endpointID}/environment_data/{environmentDataID}/scratch_pads", authAdmin, db, conf)
	RouteResource(&PushChannelsController{base}, "/apis/{apiID}/remote_endpoints/{endpointID}/push_channels", authAdmin, db, conf)
	RouteResource(&PushDevicesController{base}, "/apis/{apiID}/remote_endpoints/{endpointID}/push_channels/{pushChannelID}/push_devices", authAdmin, db, conf)
	RouteResource(&PushMessagesController{base}, "/apis/{apiID}/remote_endpoints/{endpointID}/push_channels/{pushChannelID}/push_devices/{pushDeviceID}/push_messages", authAdmin, db, conf)
	RouteResource(&SharedComponentsController{base}, "/apis/{apiID}/shared_components", authAdmin, db, conf)

	RouteStoreResource(&StoreCollectionsController{base, s}, "/store_collections", authAdmin, conf)
	RouteStoreResource(&StoreObjectsController{base, s}, "/store_collections/{collectionID}/store_objects", authAdmin, conf)

	RouteResource(&RemoteEndpointTypesController{base}, "/remote_endpoint_types", authAdmin, db, conf)

	// static assets for self-hosted systems
	admin.Handle("/{path:.*}", http.HandlerFunc(adminStaticFileHandler(conf)))

	// also add a route to the base router so that if the user leaves off the trailing slash on the admin
	// path, the client is redirected to the path that includes the trailing slash. this allows the ember
	// front-end to play nicely with us.
	adminPath := strings.TrimRight(conf.PathPrefix, "/")
	router.HandleFunc(adminPath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("%s/", adminPath), http.StatusMovedPermanently)
	})

	var public aphttp.Router
	public = aphttp.NewAccessLoggingRouter(config.Admin, conf.RequestIDHeader,
		router)
	if conf.CORSEnabled {
		public = aphttp.NewCORSAwareRouter(conf.CORSOrigin, public)
	}
	matcher := newHostMatcher(db)
	RouteSwagger(&SwaggerController{matcher}, "/swagger.json", public, db, conf)
	RoutePush(&PushController{matcher}, "/push", public, db, conf)
}

func subrouter(router *mux.Router, config config.ProxyAdmin) *mux.Router {
	adminRoute := router.NewRoute()
	if config.Host != "" {
		adminRoute = adminRoute.Host(config.Host)
	}
	if config.PathPrefix != "" {
		adminRoute = adminRoute.PathPrefix(config.PathPrefix)
	}
	return adminRoute.Subrouter()
}
