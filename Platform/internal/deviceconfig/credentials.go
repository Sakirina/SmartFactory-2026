package deviceconfig

import (
	"encoding/json"
	"errors"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func PublicConfiguration(value Result) (Result, error) {
	var update dt.DeviceConfigUpdate
	if err := protojson.Unmarshal(value.Config, &update); err != nil {
		return value, err
	}
	connector := update.GetConnectorConfig()
	if connector == nil {
		return value, errors.New("connector payload required")
	}
	connection := map[string]any{}
	for k, v := range value.Parameters.Connection {
		if k != "password" {
			connection[k] = v
		}
	}
	var err error
	connector.Connection, err = json.Marshal(connection)
	if err != nil {
		return value, err
	}
	value.Config, err = protojson.Marshal(&update)
	value.Parameters.Connection = connection
	return value, err
}
