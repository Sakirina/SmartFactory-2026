package errors

const (
	ModuleConfig    = "CFG"
	ModuleCommand   = "CMD"
	ModuleConnector = "CON"
	ModuleConverter = "CVT"
	ModuleRuntime   = "RUN"
	ModuleMQTT      = "MQT"
)

const (
	CodeConfigInvalid      = "DT-CFG-001"
	CodeConfigNotEnabled   = "DT-CFG-002"
	CodeCommandInvalid     = "DT-CMD-001"
	CodeCommandNoConnector = "DT-CMD-002"
	CodeCommandUnsupported = "DT-CMD-003"
	CodeCommandTimeout     = "DT-CMD-004"
	CodeCommandDuplicate   = "DT-CMD-005"
	CodeConnectorInvalid   = "DT-CON-001"
	CodeConnectorRuntime   = "DT-CON-002"
	CodeConverterFailed    = "DT-CVT-001"
	CodeConverterInvalid   = "DT-CVT-003"
	CodeRuntimeInvalid     = "DT-RUN-001"
	CodeMQTTDecodeFailed   = "DT-MQT-001"
)
