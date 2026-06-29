package fold

import (
	"fmt"

	"github.com/aac/act/internal/op"
)

// init registers the v0.1 op_version=1 dispatch with the op package's
// version registry. The registry lives in op (not fold) so it can be queried
// by code that does not import fold. The wrapper here type-asserts the
// generic state value back to *IssueState before delegating to ApplyDispatch.
func init() {
	op.RegisterOpVersion(1, func(state any, env op.Envelope, payload []byte, fullHash string) error {
		s, ok := state.(*IssueState)
		if !ok {
			return fmt.Errorf("fold: op_version=1 dispatch: state is %T, want *IssueState", state)
		}
		fn := ApplyDispatch(env.OpType)
		if fn == nil {
			return fmt.Errorf("fold: op_version=1 dispatch: no apply for op_type %q", env.OpType)
		}
		return fn(s, env, payload, fullHash)
	})
}
