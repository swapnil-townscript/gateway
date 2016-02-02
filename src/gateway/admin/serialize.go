package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"reflect"
	"strconv"

	"gateway/config"
	aperrors "gateway/errors"
	aphttp "gateway/http"
	"gateway/logreport"
	"gateway/model"
	apsql "gateway/sql"

	"github.com/gorilla/mux"
)

func instanceID(r *http.Request) int64 {
	return parseID(mux.Vars(r)["id"])
}

func accountIDFromPath(r *http.Request) int64 {
	return parseID(mux.Vars(r)["accountID"])
}

func accountIDFromSession(r *http.Request) int64 {
	session := requestSession(r)
	return session.Values[accountIDKey].(int64)
}

func accountIDForDevMode(db *apsql.DB) func(r *http.Request) int64 {
	return func(r *http.Request) int64 {
		account, err := model.FirstAccount(db)
		if err != nil {
			logreport.Fatal("Could not get dev mode account")
		}
		return account.ID
	}
}

func userIDDummy(r *http.Request) int64 {
	return 0
}

func userIDFromSession(r *http.Request) int64 {
	session := requestSession(r)
	return session.Values[userIDKey].(int64)
}

func userIDForDevMode(db *apsql.DB) func(r *http.Request) int64 {
	return func(r *http.Request) int64 {
		account, err := model.FirstAccount(db)
		if err != nil {
			logreport.Fatal("Could not get dev mode account")
		}
		user, err := model.FindFirstUserForAccountID(db, account.ID)
		if err != nil {
			logreport.Fatal("Could not get dev mode user")
		}
		return user.ID
	}
}

func apiIDFromPath(r *http.Request) int64 {
	return parseID(mux.Vars(r)["apiID"])
}

func endpointIDFromPath(r *http.Request) int64 {
	return parseID(mux.Vars(r)["endpointID"])
}

func testIDFromPath(r *http.Request) int64 {
	return parseID(mux.Vars(r)["testID"])
}

func mapFromPath(r *http.Request, object interface{}) {
	typ3, value := reflect.TypeOf(object), reflect.ValueOf(object)
	for i := 0; i < typ3.NumField(); i++ {
		if path := typ3.Field(i).Tag.Get("path"); path != "" {
			value.Field(i).SetInt(parseID(mux.Vars(r)[path]))
		}
	}
}

func mapAccountID(id int64, object interface{}) {
	if _, has := reflect.TypeOf(object).FieldByName("AccountID"); has {
		reflect.ValueOf(object).FieldByName("AccountID").SetInt(id)
	}
}

func mapUserID(id int64, object interface{}) {
	if _, has := reflect.TypeOf(object).FieldByName("UserID"); has {
		reflect.ValueOf(object).FieldByName("UserID").SetInt(id)
	}
}

func parseID(id string) int64 {
	i, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return -1
	}
	return i
}

func deserialize(dest interface{}, file io.Reader) aphttp.Error {
	body, err := ioutil.ReadAll(file)
	if err != nil {
		return aphttp.NewError(err, http.StatusInternalServerError)
	}

	err = json.Unmarshal(body, dest)
	if err != nil {
		return aphttp.NewError(err, http.StatusBadRequest)
	}

	return nil
}

func serialize(data interface{}, w http.ResponseWriter) aphttp.Error {
	dataJSON, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		logreport.Printf("%s Error serializing data: %v, %v", config.System, err, data)
		return aphttp.DefaultServerError()
	}

	fmt.Fprintf(w, "%s\n", string(dataJSON))
	return nil
}

// To be removed when SerializedValidationErrors are adopted everywhere
type wrappedErrors struct {
	Errors aperrors.Errors `json:"errors"`
}
