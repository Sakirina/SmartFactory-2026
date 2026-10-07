package runtime

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"context"
)

func (r *Runtime) CommandResult(ctx context.Context, id string) (*dt.CommandResult, error) {
	return r.commands.CommandResult(ctx, id)
}
