package contract_test

import (
	"testing"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestV22FieldNumbers(t *testing.T) {
	message := (&dtv1.DeviceMessage{}).ProtoReflect().Descriptor()
	assertFieldNumber(t, message, "device", 4)
	assertFieldNumber(t, message, "command_id", 6)

	configUpdate := (&dtv1.DeviceConfigUpdate{}).ProtoReflect().Descriptor()
	assertFieldNumber(t, configUpdate, "entity_revision", 5)
}

func assertFieldNumber(t *testing.T, descriptor protoreflect.MessageDescriptor, name protoreflect.Name, want protoreflect.FieldNumber) {
	t.Helper()
	field := descriptor.Fields().ByName(name)
	if field == nil {
		t.Fatalf("field %s not found", name)
	}
	if field.Number() != want {
		t.Fatalf("%s field number = %d, want %d", name, field.Number(), want)
	}
}
