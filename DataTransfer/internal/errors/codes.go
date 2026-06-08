package errors

const (
	ModuleConfig  = "CFG"
	ModuleCommand = "CMD"
	ModuleRuntime = "RUN"
	ModuleMQTT    = "MQT"
)

const (
	CodeConfigInvalid      = "DT-CFG-001"
	CodeConfigNotEnabled   = "DT-CFG-002"
	CodeCommandInvalid     = "DT-CMD-001"
	CodeCommandNoConnector = "DT-CMD-002"
	CodeCommandDuplicate   = "DT-CMD-005"
	CodeRuntimeInvalid     = "DT-RUN-001"
	CodeMQTTDecodeFailed   = "DT-MQT-001"
)
