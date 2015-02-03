package model

import (
	"encoding/json"
	"fmt"
	apsql "gateway/sql"
)

// RemoteEndpoint is an endpoint that a proxy endpoint delegates to.
type RemoteEndpoint struct {
	AccountID int64 `json:"-"`
	APIID     int64 `json:"-" db:"api_id"`

	ID              int64                            `json:"id"`
	Name            string                           `json:"name"`
	Description     string                           `json:"description"`
	Type            string                           `json:"type"`
	EnvironmentData []*RemoteEndpointEnvironmentData `json:"environment_data"`
}

// RemoteEndpointEnvironmentData contains per-environment endpoint data
type RemoteEndpointEnvironmentData struct {
	RemoteEndpointID int64           `json:"-" db:"remote_endpoint_id"`
	EnvironmentID    int64           `json:"environment_id" db:"environment_id"`
	Data             json.RawMessage `json:"data"`
}

// Validate validates the model.
func (e *RemoteEndpoint) Validate() Errors {
	errors := make(Errors)
	if e.Name == "" {
		errors.add("name", "must not be blank")
	}
	if e.Type != "http" {
		errors.add("type", "must be 'http'")
	}
	return errors
}

// ValidateFromDatabaseError translates possible database constraint errors
// into validation errors.
func (e *RemoteEndpoint) ValidateFromDatabaseError(err error) Errors {
	errors := make(Errors)
	if err.Error() == "UNIQUE constraint failed: remote_endpoints.api_id, remote_endpoints.name" ||
		err.Error() == `pq: duplicate key value violates unique constraint "remote_endpoints_api_id_name_key"` {
		errors.add("name", "is already taken")
	}
	return errors
}

// AllRemoteEndpointsForAPIIDAndAccountID returns all remoteEndpoints on the Account's API in default order.
func AllRemoteEndpointsForAPIIDAndAccountID(db *apsql.DB, apiID, accountID int64) ([]*RemoteEndpoint, error) {
	return _remoteEndpoints(db, 0, apiID, accountID)
}

// FindRemoteEndpointForAPIIDAndAccountID returns the remoteEndpoint with the id, api id, and account_id specified.
func FindRemoteEndpointForAPIIDAndAccountID(db *apsql.DB, id, apiID, accountID int64) (*RemoteEndpoint, error) {
	endpoints, err := _remoteEndpoints(db, id, apiID, accountID)
	if err != nil {
		return nil, err
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("No endpoint with id %d found", id)
	}
	return endpoints[0], nil
}

func _remoteEndpoints(db *apsql.DB, id, apiID, accountID int64) ([]*RemoteEndpoint, error) {
	query := `SELECT
	  remote_endpoints.id as id,
	  remote_endpoints.name as name,
	  remote_endpoints.description as description,
	  remote_endpoints.type as type
	FROM remote_endpoints, apis
	WHERE `
	args := []interface{}{}
	if id != 0 {
		query = query + "remote_endpoints.id = ? AND "
		args = append(args, id)
	}
	query = query +
		`   remote_endpoints.api_id = ?
	  AND remote_endpoints.api_id = apis.id
	  AND apis.account_id = ?
  ORDER BY
	  remote_endpoints.name ASC,
		remote_endpoints.id ASC;`
	args = append(args, apiID, accountID)
	remoteEndpoints := []*RemoteEndpoint{}
	err := db.Select(&remoteEndpoints, query, args...)
	if err != nil {
		return nil, err
	}
	if len(remoteEndpoints) == 0 {
		return remoteEndpoints, nil
	}

	var endpointIDs []interface{}
	for _, endpoint := range remoteEndpoints {
		endpointIDs = append(endpointIDs, endpoint.ID)
	}
	idQuery := apsql.NQs(len(remoteEndpoints))
	environmentData := []*RemoteEndpointEnvironmentData{}
	err = db.Select(&environmentData,
		`SELECT
			remote_endpoint_environment_data.remote_endpoint_id as remote_endpoint_id,
			remote_endpoint_environment_data.environment_id as environment_id,
			remote_endpoint_environment_data.data as data
		FROM remote_endpoint_environment_data, remote_endpoints, environments
		WHERE remote_endpoint_environment_data.remote_endpoint_id IN (`+idQuery+`)
			AND remote_endpoint_environment_data.environment_id = environments.id
			AND remote_endpoint_environment_data.remote_endpoint_id = remote_endpoints.id
		ORDER BY
			remote_endpoints.name ASC,
			remote_endpoints.id ASC,
			environments.name ASC;`,
		endpointIDs...)
	if err != nil {
		return nil, err
	}
	var endpointIndex int64
	for _, envData := range environmentData {
		for remoteEndpoints[endpointIndex].ID != envData.RemoteEndpointID {
			endpointIndex++
		}
		endpoint := remoteEndpoints[endpointIndex]
		endpoint.EnvironmentData = append(endpoint.EnvironmentData, envData)
	}
	return remoteEndpoints, err
}

// DeleteRemoteEndpointForAPIIDAndAccountID deletes the remoteEndpoint with the id, api_id and account_id specified.
func DeleteRemoteEndpointForAPIIDAndAccountID(tx *apsql.Tx, id, apiID, accountID int64) error {
	return tx.DeleteOne(
		`DELETE FROM remote_endpoints
		WHERE remote_endpoints.id = ?
			AND remote_endpoints.api_id IN
				(SELECT id FROM apis WHERE id = ? AND account_id = ?);`,
		id, apiID, accountID)
}

// Insert inserts the remoteEndpoint into the database as a new row.
func (e *RemoteEndpoint) Insert(tx *apsql.Tx) error {
	var err error
	e.ID, err = tx.InsertOne(
		`INSERT INTO remote_endpoints (api_id, name, description, type)
		VALUES ((SELECT id FROM apis WHERE id = ? AND account_id = ?),?,?,?)`,
		e.APIID, e.AccountID, e.Name, e.Description, e.Type)
	if err != nil {
		return err
	}
	for _, envData := range e.EnvironmentData {
		encodedData, err := envData.Data.MarshalJSON()
		if err != nil {
			return err
		}
		err = _insertRemoteEndpointEnvironmentData(tx, e.ID, envData.EnvironmentID,
			e.APIID, string(encodedData))
		if err != nil {
			return err
		}
	}
	return nil
}

// Update updates the remoteEndpoint in the database.
func (e *RemoteEndpoint) Update(tx *apsql.Tx) error {
	err := tx.UpdateOne(
		`UPDATE remote_endpoints
		SET name = ?, description = ?
		WHERE remote_endpoints.id = ?
			AND remote_endpoints.api_id IN
				(SELECT id FROM apis WHERE id = ? AND account_id = ?);`,
		e.Name, e.Description, e.ID, e.APIID, e.AccountID)
	if err != nil {
		return err
	}

	var existingEnvIDs []int64
	err = tx.Select(&existingEnvIDs,
		`SELECT environment_id
		FROM remote_endpoint_environment_data
		WHERE remote_endpoint_id = ?
		ORDER BY environment_id ASC;`,
		e.ID)

	for _, envData := range e.EnvironmentData {
		encodedData, err := envData.Data.MarshalJSON()
		if err != nil {
			return err
		}

		var found bool
		existingEnvIDs, found = popID(envData.EnvironmentID, existingEnvIDs)
		if found {
			_, err = tx.Exec(
				`UPDATE remote_endpoint_environment_data
				  SET data = ?
				WHERE remote_endpoint_id = ?
				  AND environment_id = ?;`,
				string(encodedData), e.ID, envData.EnvironmentID)
			if err != nil {
				return err
			}
		} else {
			err = _insertRemoteEndpointEnvironmentData(tx, e.ID, envData.EnvironmentID,
				e.APIID, string(encodedData))
			if err != nil {
				return err
			}
		}
	}

	if len(existingEnvIDs) == 0 {
		return nil
	}

	args := []interface{}{e.ID}
	for _, envID := range existingEnvIDs {
		args = append(args, envID)
	}
	idQuery := apsql.NQs(len(existingEnvIDs))
	_, err = tx.Exec(
		`DELETE FROM remote_endpoint_environment_data
		WHERE remote_endpoint_id = ? AND environment_id IN (`+idQuery+`);`,
		args...)

	return err
}

func _insertRemoteEndpointEnvironmentData(tx *apsql.Tx, rID, eID, apiID int64,
	data string) error {
	_, err := tx.Exec(
		`INSERT INTO remote_endpoint_environment_data
			(remote_endpoint_id, environment_id, data)
			VALUES (?, (SELECT id FROM environments WHERE id = ? AND api_id = ?), ?);`,
		rID, eID, apiID, data)
	return err
}
