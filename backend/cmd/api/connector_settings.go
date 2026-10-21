package main

import (
	"os"

	"github.com/ttobak/backend/internal/service"
)

func connectorSettingsConfig(region, poolID string) service.ConnectorSettingsConfig {
	return service.ConnectorSettingsConfig{
		ReadClientID: os.Getenv("CONNECTOR_READ_CLIENT_ID"), WriteClientID: os.Getenv("CONNECTOR_WRITE_CLIENT_ID"),
		Audience: os.Getenv("CONNECTOR_RESOURCE_AUDIENCE"),
		Issuer:   "https://cognito-idp." + region + ".amazonaws.com/" + poolID,
	}
}
