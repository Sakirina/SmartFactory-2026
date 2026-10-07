package model

import "encoding/json"

// WorkloadIdentity is public metadata. Credentials are stored separately.
type WorkloadIdentity struct {
	ID            string   `json:"id" required:"true"`
	NodeID        string   `json:"node_id" required:"true"`
	Program       string   `json:"program" required:"true"`
	Purpose       string   `json:"purpose" required:"true"`
	Enabled       bool     `json:"enabled" required:"true"`
	Capabilities  []string `json:"capabilities" required:"true"`
	ParameterIDs  []string `json:"parameter_ids" required:"true"`
	ConnectorIDs  []string `json:"connector_ids" required:"true"`
	Generation    int64    `json:"generation" required:"true" readOnly:"true"`
	Version       int64    `json:"version" required:"true" readOnly:"true"`
	InstanceID    string   `json:"instance_id,omitempty" readOnly:"true"`
	InstanceEpoch int64    `json:"instance_epoch" required:"true" readOnly:"true"`
	UpdatedMS     int64    `json:"updated_ms" required:"true" readOnly:"true"`
}

type ConfigurationReference struct {
	Kind    string `json:"kind" enum:"parameter,connector" required:"true"`
	ID      string `json:"id" required:"true" minLength:"1"`
	Version int64  `json:"version" required:"true" minimum:"1"`
	Digest  string `json:"digest" required:"true" minLength:"64" maxLength:"64"`
}

type ConfigurationEnvelope struct {
	Reference     ConfigurationReference  `json:"reference"`
	NodeID        string                  `json:"node_id"`
	Program       string                  `json:"program"`
	Purpose       string                  `json:"purpose"`
	Dynamic       bool                    `json:"dynamic"`
	Schema        map[string]any          `json:"schema,omitempty"`
	Value         any                     `json:"value,omitempty"`
	Connector     *ConnectorConfiguration `json:"connector,omitempty"`
	CredentialRef string                  `json:"credential_ref,omitempty"`
}

type ConfigurationVersion struct {
	Version int64  `json:"version" required:"true" minimum:"0"`
	Digest  string `json:"digest" required:"true"`
}

type ConfigurationReport struct {
	IdentityID     string               `json:"identity_id" required:"true"`
	NodeID         string               `json:"node_id" required:"true"`
	Program        string               `json:"program" required:"true"`
	Purpose        string               `json:"purpose" required:"true"`
	InstanceID     string               `json:"instance_id" required:"true"`
	InstanceEpoch  int64                `json:"instance_epoch" required:"true"`
	Generation     int64                `json:"generation" required:"true"`
	Sequence       int64                `json:"sequence" required:"true"`
	Kind           string               `json:"kind" enum:"parameter,connector" required:"true"`
	ID             string               `json:"id" required:"true"`
	Desired        ConfigurationVersion `json:"desired" required:"true"`
	Prepared       ConfigurationVersion `json:"prepared" required:"true"`
	Applied        ConfigurationVersion `json:"applied" required:"true"`
	Running        ConfigurationVersion `json:"running" required:"true"`
	State          string               `json:"state" enum:"prepared,running,failed,restart_required" required:"true"`
	Reason         string               `json:"reason,omitempty"`
	AtMS           int64                `json:"at_ms" required:"true"`
	EffectiveValue any                  `json:"effective_value,omitempty"`
}

type CredentialResolution struct {
	Reference     ConfigurationReference `json:"reference" required:"true"`
	CredentialRef string                 `json:"credential_ref" required:"true" minLength:"1"`
	Purpose       string                 `json:"purpose" required:"true" minLength:"1"`
}

// ConnectorRegistration contains only cloud-synchronized public metadata.
type ConnectorRegistration struct {
	Reference         ConfigurationReference `json:"reference" required:"true"`
	Configuration     ConnectorConfiguration `json:"configuration" required:"true"`
	NodeID            string                 `json:"node_id" required:"true"`
	NodeVersion       int64                  `json:"node_version" required:"true"`
	SourceURL         string                 `json:"source_url" required:"true"`
	CertificateSHA256 string                 `json:"certificate_sha256" required:"true"`
}

type ConnectorCredentialRequest struct {
	Resolution    CredentialResolution        `json:"resolution" required:"true"`
	ReleaseTarget *ReleaseConfigurationTarget `json:"release_target,omitempty"`
}

type ConfigurationMetadata struct {
	Reference            ConfigurationReference `json:"reference" required:"true"`
	Program              string                 `json:"program" required:"true"`
	NodeIDs              []string               `json:"node_ids" required:"true"`
	GroupID              string                 `json:"group_id,omitempty"`
	CredentialRef        string                 `json:"credential_ref,omitempty"`
	RequiredCapabilities []string               `json:"required_capabilities,omitempty"`
}

type ReleaseConfigurationTarget struct {
	DeploymentID string                   `json:"deployment_id" required:"true"`
	Generation   int64                    `json:"generation" required:"true" minimum:"1"`
	References   []ConfigurationReference `json:"references" required:"true"`
}

type ReleaseConfigurationTargetProof struct {
	Identity     WorkloadIdentity         `json:"identity"`
	DeploymentID string                   `json:"deployment_id"`
	Generation   int64                    `json:"generation"`
	References   []ConfigurationReference `json:"references"`
}

// CredentialPayload is only returned by the authenticated internal resolver.
type CredentialPayload struct {
	Reference     ConfigurationReference `json:"reference"`
	CredentialRef string                 `json:"credential_ref"`
	Payload       json.RawMessage        `json:"payload"`
}
