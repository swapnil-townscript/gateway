package model

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"

	"gateway/config"
	aperrors "gateway/errors"
	"gateway/model/remote_endpoint"
	"gateway/soap"
	apsql "gateway/sql"

	"github.com/vincent-petithory/dataurl"
)

const soapRemoteEndpoints = "soap_remote_endpoints"

// SoapRemoteEndpoint contains attributes for a remote endpoint of type Soap
type SoapRemoteEndpoint struct {
	RemoteEndpointID int64 `json:"-" db:"remote_endpoint_id"`

	ID                     int64  `json:"-" db:"id"`
	Wsdl                   string `json:"-"`
	generatedJar           []byte
	GeneratedJarThumbprint string `json:"-" db:"generated_jar_thumbprint"`

	RemoteEndpoint *RemoteEndpoint `json:"-"`
}

// Copy creates a shallow copy of a SoapRemoteEndpoint and returns a reference to the copy
func (e *SoapRemoteEndpoint) Copy() *SoapRemoteEndpoint {
	return &SoapRemoteEndpoint{
		RemoteEndpointID:       e.RemoteEndpointID,
		ID:                     e.ID,
		Wsdl:                   e.Wsdl,
		generatedJar:           e.generatedJar,
		GeneratedJarThumbprint: e.GeneratedJarThumbprint,
		RemoteEndpoint:         e.RemoteEndpoint,
	}
}

type soapNotificationListener struct {
	*apsql.DB
}

// Notify tells the listener a particular notification was fired
func (l *soapNotificationListener) Notify(n *apsql.Notification) {
	if n.Table != soapRemoteEndpoints {
		return
	}

	switch n.Event {
	case apsql.Update, apsql.Insert:
		err := cacheJarFile(l.DB, n.APIID)
		if err != nil {
			log.Printf("%s Error caching jarfile for api %d: %v", config.System, n.APIID, err)
		}
	case apsql.Delete:
		var remoteEndpointID int64
		var ok bool
		if remoteEndpointID, ok = n.Messages[0].(int64); !ok {
			tmp, ok := n.Messages[0].(float64)
			remoteEndpointID = int64(tmp)

			if !ok {
				log.Printf("%s Error deleting jarfile for api %d: %v", config.System, n.APIID, "deletion message did not come in expected format")
				return
			}
		}

		err := DeleteJarFile(remoteEndpointID)

		if err != nil {
			log.Printf("%s Error deleting jarfile for api %d: %v", config.System, n.APIID, err)
		}
	}
}

// Reconnect tells the listener that we may have been disconnected, but
// have reconnected. They should update all state that could have changed.
func (l *soapNotificationListener) Reconnect() {
	// Nothing to do here
}

func writeToJarFile(bytes []byte, filename string) error {
	return ioutil.WriteFile(filename, bytes, os.ModeDir|0600)
}

func DeleteJarFile(soapRemoteEndpointID int64) error {
	log.Printf("Received a request to delete jar file for soapRemoteEndpointID %d", soapRemoteEndpointID)
	jarDir, err := soap.EnsureJarPath()
	if err != nil {
		return err
	}

	jarFileName := path.Join(jarDir, fmt.Sprintf("%d.jar", soapRemoteEndpointID))
	return os.Remove(jarFileName)
}

func cacheJarFile(db *apsql.DB, soapRemoteEndpointID int64) error {
	jarDir, err := soap.EnsureJarPath()
	if err != nil {
		return err
	}

	// check if jar exists
	jarFileName := path.Join(jarDir, fmt.Sprintf("%d.jar", soapRemoteEndpointID))
	fileBytes, err := ioutil.ReadFile(jarFileName)

	var hexsum string
	switch {
	case err != nil && os.IsNotExist(err):
		// copy to file system
		hexsum = ""
	case err != nil:
		return fmt.Errorf("Unable to open jar file: %v", err)
	default:
		// jar exists! get its MD5 hash and compare against record from DB.
		checksum := md5.Sum(fileBytes)
		hexsum = hex.EncodeToString(checksum[:])
	}

	fileBytes, err = GetGeneratedJarBytes(db, soapRemoteEndpointID, hexsum)
	if err != nil {
		return fmt.Errorf("Unable to get bytes from database: %v", err)
	}

	if fileBytes != nil {
		// copy to file system
		err := writeToJarFile(fileBytes, jarFileName)
		if err != nil {
			return fmt.Errorf("Unable to write jar %s to file system: %v", jarFileName, err)
		}
	}

	return nil
}

// StartSoapRemoteEndpointUpdateListener registers a listener for updates from
func StartSoapRemoteEndpointUpdateListener(db *apsql.DB) {
	listener := &soapNotificationListener{db}
	db.RegisterListener(listener)
}

// NewSoapRemoteEndpoint creates a new SoapRemoteEndpoint struct
func NewSoapRemoteEndpoint(remoteEndpoint *RemoteEndpoint) (SoapRemoteEndpoint, error) {
	soap := SoapRemoteEndpoint{RemoteEndpointID: remoteEndpoint.ID, RemoteEndpoint: remoteEndpoint}

	data := remoteEndpoint.Data
	soapConfig, err := remote_endpoint.SoapConfig(data)
	if err != nil {
		return soap, fmt.Errorf("Encountered an error attempting to extract soap config: %v", err)
	}

	if soapConfig.WSDL != "" {
		decoded, err := dataurl.DecodeString(soapConfig.WSDL)
		if err != nil {
			return soap, fmt.Errorf("Encountered an error attempting to decode WSDL: %v", err)
		}

		soap.Wsdl = string(decoded.Data)
	}

	return soap, nil
}

// GetGeneratedJarBytes returns the bytes of the generated_jar file for the soap_remote_endpoint with the specified ID,
// if and only if the checksum does not match the generated_jar_thumbprint stored in the DB.  If the thumbprints match,
// then they are the same file, and so no bytes will be returned.
func GetGeneratedJarBytes(db *apsql.DB, soapRemoteEndpointID int64, checksum string) ([]byte, error) {
	query := `SELECT generated_jar FROM soap_remote_endpoints WHERE id = ? AND generated_jar_thumbprint != ?`

	var dest []byte
	err := db.Get(&dest, query, soapRemoteEndpointID, checksum)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("Unable to get generated jar bytes from soap_remote_entpoints for ID %v: %v", soapRemoteEndpointID, err)
	}

	return dest, nil
}

// Insert inserts a new SoapRemoteEndpoint record into the database
func (e *SoapRemoteEndpoint) Insert(tx *apsql.Tx) error {
	query := `INSERT INTO soap_remote_endpoints(remote_endpoint_id, wsdl)
            VALUES (?, ?)`

	var err error
	id, err := tx.InsertOne(query, e.RemoteEndpointID, e.Wsdl)
	if err != nil {
		return fmt.Errorf("Unable to insert SoapRemoteEndpoint record %v: %v", e.RemoteEndpointID, err)
	}
	e.ID = id

	tx.PostCommit = func(tx *apsql.Tx) {
		// copy to ensure there's no chance of concurrency issues
		go afterSave(tx, e.Copy())
	}

	return tx.Notify(
		soapRemoteEndpoints,
		e.RemoteEndpoint.AccountID,
		e.RemoteEndpoint.UserID,
		e.RemoteEndpoint.APIID,
		e.ID,
		apsql.Insert,
	)
}

// FindSoapRemoteEndpointByRemoteEndpointID finds a SoapRemoteEndpoint given a remoteEndpoint
func FindSoapRemoteEndpointByRemoteEndpointID(db *apsql.DB, remoteEndpointID int64) (*SoapRemoteEndpoint, error) {
	query := `SELECT id, remote_endpoint_id, generated_jar_thumbprint
            FROM soap_remote_endpoints
            WHERE remote_endpoint_id = ?`

	soapRemoteEndpoint := &SoapRemoteEndpoint{}
	if err := db.Get(soapRemoteEndpoint, query, remoteEndpointID); err != nil {
		return nil, fmt.Errorf("Unable to fetch SoapRemoteEndpoint from the database for remote_endpoint_id %d: %v", remoteEndpointID, err)
	}

	return soapRemoteEndpoint, nil
}

// Update updates an existing SoapRemoteEndpoint record in the database
func (e *SoapRemoteEndpoint) Update(tx *apsql.Tx) error {
	return e.update(tx, true, false)
}

func (e *SoapRemoteEndpoint) update(tx *apsql.Tx, fireAfterSave, updateGeneratedJar bool) error {
	var query string
	if updateGeneratedJar {
		query = `UPDATE soap_remote_endpoints
              SET wsdl = ?, generated_jar = ?, generated_jar_thumbprint = ?
              WHERE id = ?`
	} else {
		query = `UPDATE soap_remote_endpoints
              SET wsdl = ?
              WHERE id = ?`
	}

	var err error
	if updateGeneratedJar {
		err = tx.UpdateOne(query, e.Wsdl, e.generatedJar, e.GeneratedJarThumbprint, e.ID)
	} else {
		err = tx.UpdateOne(query, e.Wsdl, e.ID)
	}

	if err != nil {
		return fmt.Errorf("Unable to update SoapRemoteEndpoint: %v", err)
	}

	if fireAfterSave {
		tx.PostCommit = func(tx *apsql.Tx) {
			// copy to ensure there's no chance of concurrency issues
			go afterSave(tx, e.Copy())
		}
	}

	return tx.Notify(soapRemoteEndpoints, e.RemoteEndpoint.AccountID, e.RemoteEndpoint.UserID, e.RemoteEndpoint.APIID, e.ID, apsql.Update)
}

func afterSave(origTx *apsql.Tx, e *SoapRemoteEndpoint) {
	db := origTx.DB
	processWsdl(db, e)
}

func processWsdl(db *apsql.DB, e *SoapRemoteEndpoint) {
	e.RemoteEndpoint.Status = apsql.MakeNullString(RemoteEndpointStatusProcessing)
	e.RemoteEndpoint.StatusMessage = apsql.MakeNullStringNull()

	// In a new transaction, update the status to processing before we do anything
	err := db.DoInTransaction(func(tx *apsql.Tx) error {
		err := e.RemoteEndpoint.update(tx, false)
		if err != nil {
			return aperrors.NewWrapped("soap_remote_endpoint.go: updating remote endpoint", err)
		}
		err = e.update(tx, false, false)
		if err != nil {
			return aperrors.NewWrapped("soap_remote_endpoint.go: updating soap remote endpoint", err)
		}
		return nil
	})

	if err != nil {
		log.Printf("%s Unable to update status to %v.  Processing will continue anyway.", config.System, RemoteEndpointStatusProcessing)
	}

	err = db.DoInTransaction(func(tx *apsql.Tx) error {
		return ingestWsdl(tx, e)
	})

	if err != nil {
		log.Printf("%s WSDL could not be processed due to error encountered:  %v", config.System, err)
		e.RemoteEndpoint.Status = apsql.MakeNullString(RemoteEndpointStatusFailed)
		e.RemoteEndpoint.StatusMessage = apsql.MakeNullString(err.Error())
	} else {
		e.RemoteEndpoint.Status = apsql.MakeNullString(RemoteEndpointStatusSuccess)
		e.RemoteEndpoint.StatusMessage = apsql.MakeNullStringNull()
	}

	err = db.DoInTransaction(func(tx *apsql.Tx) error {
		err := e.RemoteEndpoint.update(tx, false)
		if err != nil {
			return aperrors.NewWrapped("soap_remote_endpoint.go: Updating final state on remote endpoint", err)
		}
		err = e.update(tx, false, false)
		if err != nil {
			return aperrors.NewWrapped("soap_remote_endpoint.go: Updating final state on soap remote endpoint", err)
		}
		return nil
	})

	if err != nil {
		log.Printf("%s Unable to update status of RemoteSoapEndpoint due to error: %v", config.System, err)
	}
}

func ingestWsdl(tx *apsql.Tx, e *SoapRemoteEndpoint) error {
	log.Printf("%s Starting wsdl ingestion", "[debug]")

	dir, err := soap.EnsureJarPath()
	if err != nil {
		return err
	}

	// write wsdl file to directory
	filePerm := os.FileMode(os.ModeDir | 0600)
	filename := path.Join(dir, fmt.Sprintf("%d.wsdl", e.ID))

	err = ioutil.WriteFile(filename, []byte(e.Wsdl), filePerm)
	if err != nil {
		return err
	}

	// invoke wsimport
	outputfile := path.Join(dir, fmt.Sprintf("%d.jar", e.ID))
	err = soap.Wsimport(filename, outputfile)
	if err != nil {
		log.Printf("%s Tried to invoke wsimport: %v", "[debug]", err)
		return err
	}

	// update the DB with the generated jars bytes!
	bytes, err := ioutil.ReadFile(outputfile)
	if err != nil {
		log.Printf("%s Couldn't read bytes! %v", "[debug]", err)
		return err
	}
	e.generatedJar = bytes
	checksum := md5.Sum(bytes)
	e.GeneratedJarThumbprint = hex.EncodeToString(checksum[:])

	err = e.update(tx, false, true)
	if err != nil {
		return err
	}

	err = tx.Notify(soapRemoteEndpoints, e.RemoteEndpoint.AccountID, e.RemoteEndpoint.UserID, e.RemoteEndpoint.APIID, e.ID, apsql.Update)
	if err != nil {
		log.Printf("%s Unable to notify of update: %v", "[debug]", err)
		return err
	}

	// clean up wsdl
	err = os.Remove(filename)
	if err != nil {
		return err
	}

	log.Printf("%s Finished wsdl ingestion", "[debug]")

	return nil
}