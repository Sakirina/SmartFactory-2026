package configcenter

import (
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"context"
	"errors"
)

// PinLocalRelease runs after staged manifest validation and before workers.
// Previous release-managed consumers are removed only when their runtime still
// names that source. Independently managed configuration is preserved.
func (s *Service) PinLocalRelease(ctx context.Context, envelopes []model.ConfigurationEnvelope) error {
	desired := map[string]model.ConfigurationReference{}
	for _, e := range envelopes {
		desired[e.Reference.Kind+":"+e.Reference.ID] = e.Reference
	}
	old, err := s.Store.List(ctx, "configuration_local_release_target")
	if err != nil {
		return err
	}
	for _, d := range old {
		if _, ok := desired[d.ID]; ok {
			continue
		}
		var ref model.ConfigurationReference
		if err = store.DecodeJSON(d.Data, &ref); err != nil {
			return err
		}
		if ref.Kind == "connector" && s.RemoveConnectorRuntime != nil {
			if err = s.RemoveConnectorRuntime(ctx, ref); err != nil {
				return err
			}
		}
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		for _, d := range old {
			if _, ok := desired[d.ID]; ok {
				continue
			}
			var ref model.ConfigurationReference
			if e := store.DecodeJSON(d.Data, &ref); e != nil {
				return e
			}
			if runtime, e := tx.Get("configuration_runtime", d.ID); e == nil {
				var prior model.ConfigurationEnvelope
				if e = store.DecodeJSON(runtime.Data, &prior); e != nil {
					return e
				}
				if prior.Reference == ref {
					if e = tx.Delete("configuration_runtime", d.ID); e != nil {
						return e
					}
					if e = tx.Delete("configuration_prepared", d.ID); e != nil {
						return e
					}
					if ref.Kind == "parameter" {
						if p, e := tx.Get("parameter", ref.ID); e == nil {
							previous, e := store.Decode[Parameter](p)
							if e != nil {
								return e
							}
							if previous.Version == ref.Version && previous.ContentDigest == ref.Digest {
								if e = tx.Delete("parameter", ref.ID); e != nil {
									return e
								}
							}
						} else if !errors.Is(e, store.ErrNotFound) {
							return e
						}
					}
				}
			} else if !errors.Is(e, store.ErrNotFound) {
				return e
			}
			if e := tx.Delete("configuration_local_release_target", d.ID); e != nil {
				return e
			}
		}
		for id, ref := range desired {
			if e := tx.SetEphemeral("configuration_local_release_target", id, ref); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.ApplyPolicy(ctx)
}
