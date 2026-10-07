package configcenter

import (
	"context"
	"errors"
	"fmt"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *Service) ConfigurationMetadata(ctx context.Context, refs []model.ConfigurationReference) ([]model.ConfigurationMetadata, error) {
	if e := s.EnsureConnectorVersions(ctx, refs); e != nil {
		return nil, e
	}
	out := make([]model.ConfigurationMetadata, 0, len(refs))
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		for _, ref := range refs {
			if ref.ID == "" || ref.Version < 1 {
				return errors.New("fixed configuration identity and version required")
			}
			m := model.ConfigurationMetadata{Reference: ref, NodeIDs: []string{}}
			switch ref.Kind {
			case "parameter":
				d, e := tx.Read("parameter_version", ref.ID+":"+fmt.Sprint(ref.Version))
				if e != nil {
					return e
				}
				p, e := store.Decode[Parameter](d)
				if e != nil {
					return e
				}
				m.Reference.Digest = ParameterDigest(p)
				m.Program = p.Program
				m.NodeIDs = p.TargetNodeIDs
				if m.NodeIDs == nil {
					m.NodeIDs = []string{}
				}
				m.CredentialRef = parameterEnvelope(p, model.WorkloadIdentity{}).CredentialRef
			case "connector":
				d, e := tx.Read("connector_configuration_version", model.ConnectorSecretReference(ref.ID, ref.Version))
				if e != nil {
					return e
				}
				c, e := store.Decode[model.ConnectorConfiguration](d)
				if e != nil {
					return e
				}
				m.Reference.Digest = ConnectorDigest(c)
				m.Program = "gateway"
				m.RequiredCapabilities = []string{"connector:" + c.Protocol}
				m.NodeIDs = []string{c.EdgeID}
				m.GroupID = c.GroupID
				m.CredentialRef = c.CredentialRef
			default:
				return errors.New("unsupported configuration kind")
			}
			if ref.Digest != "" && ref.Digest != m.Reference.Digest {
				return store.ErrConflict
			}
			out = append(out, m)
		}
		return nil
	})
	return out, err
}
