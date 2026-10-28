package runner

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/delvivo/dumpling/internal/store"
)

// decision is what a plan run produces for one database.
type decision struct {
	Kind   string       // "full" or "incr"
	Head   store.Backup // newest link (incr only)
	Full   store.Backup // the chain's full (incr only)
	Reason string       // why an incremental request became a full ("" otherwise)
}

// decideKind applies research R2 rules 1–4 to an incremental request; anything else is a full.
func (r *Runner) decideKind(p store.Plan, db, requested string) (decision, error) {
	if requested != "incremental" || !p.Incremental() {
		return decision{Kind: "full"}, nil
	}
	head, ok, err := r.St.ChainHead(p.ID, db)
	if err != nil {
		return decision{}, err
	}
	if !ok {
		return decision{Kind: "full", Reason: "no base"}, nil
	}
	if head.Status != "ok" {
		return decision{Kind: "full", Reason: fmt.Sprintf("chain broken: link %s is %s", head.ID, head.Status)}, nil
	}
	anc, err := r.St.Ancestors(head)
	var ce *store.ChainError
	if errors.As(err, &ce) {
		return decision{Kind: "full", Reason: "chain broken: " + ce.Error()}, nil
	}
	if err != nil {
		return decision{}, err
	}
	full := head
	if len(anc) > 0 {
		full = anc[0]
	}
	if head.Seq >= p.MaxChain {
		return decision{Kind: "full", Reason: fmt.Sprintf("chain length limit (%d)", p.MaxChain)}, nil
	}
	if p.ChainResetAt != "" && p.ChainResetAt > full.StartedAt {
		return decision{Kind: "full", Reason: "plan changed"}, nil
	}
	return decision{Kind: "incr", Head: head, Full: full}, nil
}

// linkPassphrase resolves the passphrase of one link: request → unlocked passphrase → plan.
// An unavailable one is an error naming the link (backup_locked for imported links).
func (r *Runner) linkPassphrase(b store.Backup, requested string) (string, error) {
	if !b.Encrypted {
		return "", nil
	}
	if requested != "" {
		return requested, nil
	}
	if p := b.PassphrasePlain(); p != "" {
		return p, nil
	}
	if b.PlanID != "" {
		if pl, err := r.St.Plan(b.PlanID); err == nil && pl.PassphrasePlain() != "" {
			return pl.PassphrasePlain(), nil
		}
	}
	e := errf(http.StatusBadRequest, "passphrase_required", "backup %s is encrypted: passphrase required", b.ID)
	if b.Locked {
		e = errf(http.StatusBadRequest, "backup_locked", "imported encrypted backup %s: unlock it or enter its passphrase", b.ID)
	}
	e.Extra = map[string]any{"link": b.ID}
	return "", e
}
