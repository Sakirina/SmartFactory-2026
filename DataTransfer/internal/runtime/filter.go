package runtime

import dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"

type Filter struct {
	DeviceIDs    map[string]struct{}
	DeviceTypes  map[string]struct{}
	ConnectorIDs map[string]struct{}
	Types        map[dtv1.MessageType]struct{}
	TagMatch     map[string]string
}

func FilterFromSubscribeRequest(req *dtv1.SubscribeRequest, defaultTypes []dtv1.MessageType) Filter {
	if req == nil {
		return Filter{Types: typeSet(nil, defaultTypes)}
	}
	return Filter{
		DeviceIDs:    stringSet(req.DeviceIds),
		DeviceTypes:  stringSet(req.DeviceTypes),
		ConnectorIDs: stringSet(req.ConnectorIds),
		Types:        typeSet(req.Types, defaultTypes),
		TagMatch:     copyStringMap(req.TagMatch),
	}
}

func (f Filter) Match(msg *dtv1.DeviceMessage) bool {
	if msg == nil {
		return false
	}
	if len(f.Types) > 0 {
		if _, ok := f.Types[msg.Type]; !ok {
			return false
		}
	}
	device := msg.GetDevice()
	if len(f.DeviceIDs) > 0 {
		if _, ok := f.DeviceIDs[device.GetDeviceId()]; !ok {
			return false
		}
	}
	if len(f.DeviceTypes) > 0 {
		if _, ok := f.DeviceTypes[device.GetDeviceType()]; !ok {
			return false
		}
	}
	if len(f.ConnectorIDs) > 0 {
		if _, ok := f.ConnectorIDs[device.GetConnectorId()]; !ok {
			return false
		}
	}
	for key, expected := range f.TagMatch {
		if device.GetTags()[key] != expected {
			return false
		}
	}
	return true
}

func stringSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func typeSet(values []dtv1.MessageType, defaults []dtv1.MessageType) map[dtv1.MessageType]struct{} {
	source := values
	if len(source) == 0 {
		source = defaults
	}
	if len(source) == 0 {
		return nil
	}
	out := make(map[dtv1.MessageType]struct{}, len(source))
	for _, value := range source {
		out[value] = struct{}{}
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
