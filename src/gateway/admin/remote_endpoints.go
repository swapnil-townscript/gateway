package admin

import (
	"encoding/json"
	"fmt"
	"gateway/config"
	"gateway/model"
	apsql "gateway/sql"
	"log"

	"github.com/jmoiron/sqlx/types"
)

// BeforeInsert does some work before inserting a RemoteEndpoint
func (c *RemoteEndpointsController) BeforeInsert(remoteEndpoint *model.RemoteEndpoint, tx *apsql.Tx) error {
	if remoteEndpoint.Type != model.RemoteEndpointTypeSoap {
		return nil
	}

	remoteEndpoint.Status = apsql.MakeNullString(model.RemoteEndpointStatusPending)
	soap, err := model.NewSoapRemoteEndpoint(remoteEndpoint)
	if err != nil {
		return fmt.Errorf("Unable to construct SoapRemoteEndpoint object: %v", err)
	}

	remoteEndpoint.Soap = &soap
	remoteEndpoint.Data = types.JsonText(json.RawMessage([]byte("null")))

	return nil
}

// BeforeUpdate does some work before updataing a RemoteEndpoint
func (c *RemoteEndpointsController) BeforeUpdate(remoteEndpoint *model.RemoteEndpoint, tx *apsql.Tx) error {
	if remoteEndpoint.Status.String == model.RemoteEndpointStatusPending {
		return fmt.Errorf("Unable to update remote endpoint -- status is currently %s", model.RemoteEndpointStatusPending)
	}

	if remoteEndpoint.Type != model.RemoteEndpointTypeSoap {
		return nil
	}

	soap, err := model.NewSoapRemoteEndpoint(remoteEndpoint)
	if err != nil {
		return fmt.Errorf("Unable to construct SoapRemoteEndpoint object for update: %v", err)
	}

	if soap.Wsdl == "" {
		return nil
	}

	soapRemoteEndpoint, err := model.FindSoapRemoteEndpointByRemoteEndpointID(tx.DB, remoteEndpoint.ID)
	if err != nil {
		return fmt.Errorf("Unable to fetch SoapRemoteEndpoint with remote_endpoint_id of %d: %v", remoteEndpoint.ID, err)
	}

	remoteEndpoint.Soap = soapRemoteEndpoint
	remoteEndpoint.Data = types.JsonText(json.RawMessage([]byte("null")))

	soapRemoteEndpoint.Wsdl = soap.Wsdl
	soapRemoteEndpoint.GeneratedJarThumbprint = ""
	soapRemoteEndpoint.RemoteEndpoint = remoteEndpoint

	remoteEndpoint.Status = apsql.MakeNullString(model.RemoteEndpointStatusPending)
	remoteEndpoint.StatusMessage = apsql.MakeNullStringNull()

	return nil
}

// AfterInsert does some work after inserting a RemoteEndpoint
func (c *RemoteEndpointsController) AfterInsert(remoteEndpoint *model.RemoteEndpoint, tx *apsql.Tx) error {
	if remoteEndpoint.Type != model.RemoteEndpointTypeSoap {
		return nil
	}

	remoteEndpoint.Soap.RemoteEndpointID = remoteEndpoint.ID
	err := remoteEndpoint.Soap.Insert(tx)
	if err != nil {
		return fmt.Errorf("Unable to insert SoapRemoteEndpoint: %v", err)
	}

	return nil
}

// AfterUpdate does some work after updating a RemoteEndpoint
func (c *RemoteEndpointsController) AfterUpdate(remoteEndpoint *model.RemoteEndpoint, tx *apsql.Tx) error {
	if remoteEndpoint.Type != model.RemoteEndpointTypeSoap {
		return nil
	}

	err := remoteEndpoint.Soap.Update(tx)
	if err != nil {
		return fmt.Errorf("Unable to update SoapRemoteEndpoint: %v", err)
	}

	return nil
}

// BeforeDelete does some work before delete
func (c *RemoteEndpointsController) BeforeDelete(remoteEndpoint *model.RemoteEndpoint, tx *apsql.Tx) error {
	if remoteEndpoint.Status.String == model.RemoteEndpointStatusPending {
		return fmt.Errorf("Unable to delete remote endpoint -- status is currently %s", model.RemoteEndpointStatusPending)
	}
	return nil
}

// AfterDelete does some work after delete
func (c *RemoteEndpointsController) AfterDelete(remoteEndpoint *model.RemoteEndpoint, tx *apsql.Tx) error {
	if remoteEndpoint.Type != model.RemoteEndpointTypeSoap {
		return nil
	}

	var err error
	remoteEndpoint.Soap, err = model.FindSoapRemoteEndpointByRemoteEndpointID(tx.DB, remoteEndpoint.ID)
	if err != nil {
		// we can ignore this -- only result will be that we won't be able to delete the file on the file
		// system -- no big deal
		return nil
	}

	err = model.DeleteJarFile(remoteEndpoint.Soap.ID)
	if err != nil {
		log.Printf("%s Unable to delete jar file for SoapRemoteEndpoint: %v", config.System, err)
	}

	err = tx.Notify("soap_remote_endpoints", remoteEndpoint.Soap.ID, apsql.Delete)
	if err != nil {
		log.Printf("%s Failed to send notification that soap_remote_endpoint was deleted for id %d: %v", config.System, remoteEndpoint.Soap.ID, err)
	}

	return nil
}
