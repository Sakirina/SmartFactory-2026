package simulator

import (
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	"strings"
	"time"
)

type namespace struct {
	*server.MapNamespace
	state *State
}

func (n *namespace) Attribute(id *ua.NodeID, attribute ua.AttributeID) *ua.DataValue {
	parts := strings.SplitN(id.StringID(), ".", 2)
	if attribute == ua.AttributeIDValue && len(parts) == 2 {
		values := n.state.Snapshot()
		fields := values[parts[0]]
		if value, ok := fields[parts[1]]; ok {
			return &ua.DataValue{EncodingMask: ua.DataValueValue | ua.DataValueStatusCode | ua.DataValueSourceTimestamp, Value: ua.MustVariant(value), Status: ua.StatusOK, SourceTimestamp: time.Now()}
		}
		return &ua.DataValue{EncodingMask: ua.DataValueStatusCode, Status: ua.StatusBadNodeIDUnknown}
	}
	n.Mu.RLock()
	defer n.Mu.RUnlock()
	return n.MapNamespace.Attribute(id, attribute)
}
func (n *namespace) SetAttribute(id *ua.NodeID, attribute ua.AttributeID, value *ua.DataValue) ua.StatusCode {
	parts := strings.SplitN(id.StringID(), ".", 2)
	if attribute != ua.AttributeIDValue || len(parts) != 2 || value == nil || value.Value == nil {
		return ua.StatusBadWriteNotSupported
	}
	if parts[1] != "extractor" {
		return ua.StatusBadNotWritable
	}
	if n.state.Snapshot()[parts[0]]["interlock"] == true {
		return ua.StatusBadUserAccessDenied
	}
	if e := n.state.Set(parts[0], parts[1], value.Value.Value()); e != nil {
		return ua.StatusBadTypeMismatch
	}
	n.ChangeNotification(id.StringID())
	return ua.StatusOK
}
