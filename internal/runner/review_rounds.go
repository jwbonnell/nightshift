package runner

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// roundRec records one fix round's outcome, kept on the run so finish's
// result note can list every round oldest-first (see resultBody).
type roundRec struct {
	round    int
	verdict  string
	notePath string
	noteErr  error
}

// reviewRound runs fix-round review number st.ReviewRound+1 against the
// clone's last_green commit, publishing it to the source repo only for the
// round's duration (publishReviewRef/deleteReviewRef in state.go — the host
// never runs git against the clone itself). It records the round's outcome
// on r (reviewVerdict/reviewNote/reviewNoteErr, plus an entry in
// reviewRounds) for finish to report, and returns whether the round handed
// blocking findings back to the agent via REVIEW.md (true) rather than
// letting the run complete (false).
func (r *run) reviewRound() bool {
	sp := r.spec
	st := r.st
	clock := r.opts.clockFn()
	round := st.ReviewRound + 1

	names := sp.Lenses
	if len(names) == 0 {
		names = []string{"general"}
	}

	if err := r.publishReviewRef(); err != nil {
		r.logf("review round %d: publish failed: %v", round, err)
		notePath, noteErr := writeReviewNoteLenses(r.opts.Home, sp.Slug, "error", "", short(st.Base), sp.ReviewModel, names, round, "Review failed: "+err.Error(), r.opts.Now)
		r.recordReviewRound(round, "error", notePath, noteErr)
		return false
	}
	defer r.deleteReviewRef()

	rv := r.reviewer()
	ctx, cancel := r.iterCtx(r.deadline, clock)
	defer cancel()

	params := reviewParams{
		home:      r.opts.Home,
		stateRoot: r.opts.StateRoot,
		slug:      sp.Slug,
		repo:      sp.Repo,
		base:      st.Base,
		ref:       reviewRoundRef(sp.Slug),
		specPath:  sp.Path,
		progress:  r.scaffoldRead(r.progressPath()),
		summary: reviewSummary(fmt.Sprintf("in progress (review round %d of up to %d)", round, sp.ReviewRounds+1),
			strconv.Itoa(st.Iterations), strconv.Itoa(st.Commits), short(st.Base), short(st.LastGreen), st.Branch),
		model:    sp.ReviewModel,
		lenses:   sp.Lenses,
		round:    round,
		now:      r.opts.Now,
		ctxBase:  r.opts.Ctx,
		deadline: r.deadline,
		clock:    clock,
		sleep:    r.opts.Sleep,
		limits:   r.opts.limitCfg(),
		ctxFn: func() (context.Context, context.CancelFunc) {
			return r.iterCtx(r.deadline, clock)
		},
	}

	results, resolvedNames, head, buildErr := reviewLenses(ctx, rv, params)
	if buildErr != nil {
		notePath, noteErr := writeReviewNoteLenses(params.home, params.slug, "error", short(head), short(params.base), params.model, resolvedNames, round, "Review failed: "+buildErr.Error(), params.now)
		r.logf("review round %d: error: %v", round, buildErr)
		r.recordReviewRound(round, "error", notePath, noteErr)
		return false
	}

	verdicts := make([]string, len(results))
	for i, res := range results {
		verdicts[i] = res.verdict
	}
	verdict := combineVerdicts(verdicts)
	body := reviewNoteBody(results)
	notePath, noteErr := writeReviewNoteLenses(params.home, params.slug, verdict, short(head), short(params.base), params.model, resolvedNames, round, body, params.now)
	r.recordReviewRound(round, verdict, notePath, noteErr)

	items := blockingItems(results, round)
	if len(items) == 0 || st.ReviewHandbacks >= sp.ReviewRounds {
		r.logf("review round %d: %s, complete", round, verdict)
		return false
	}

	r.handBack(round, verdict, items)
	return true
}

// recordReviewRound stashes round's outcome both on reviewRounds (for
// finish's "## Review" listing, oldest first) and on the single
// reviewVerdict/reviewNote/reviewNoteErr fields runAutoReview also sets.
// finish only calls runAutoReview when no round ran, so these never
// disagree about which review's outcome resultBody/notifyText should use
// outside the "## Review" listing itself.
func (r *run) recordReviewRound(round int, verdict, notePath string, noteErr error) {
	r.reviewRounds = append(r.reviewRounds, roundRec{round: round, verdict: verdict, notePath: notePath, noteErr: noteErr})
	r.reviewVerdict = verdict
	r.reviewNote = notePath
	r.reviewNoteErr = noteErr
}

// handBack writes REVIEW.md, removes .spec-complete, appends to
// PROGRESS.md and advances the round/handback counters — the mechanism by
// which a rejecting fix round sends the agent back to work instead of
// letting the run complete.
func (r *run) handBack(round int, verdict string, items []blockItem) {
	st := r.st
	var b strings.Builder
	fmt.Fprintf(&b, "# Review round %d — blocking findings\n\nFix each item below, then note its id in PROGRESS.md and touch .spec-complete again.\n\n", round)
	for _, it := range items {
		fmt.Fprintf(&b, "- **%s** (%s) %s\n", it.ID, it.Lens, it.Text)
	}
	r.scaffoldWrite(r.reviewPath(), b.String())
	r.scaffoldRemove(r.completePath())
	r.appendProgress(fmt.Sprintf("review round %d: %d blocking finding(s) in REVIEW.md — fix each, note its id here, then touch .spec-complete again", round, len(items)))
	r.logf("review round %d: %s, %d blocking — handed back", round, verdict, len(items))
	st.ReviewHandbacks++
	st.ReviewRound = round
	_ = r.saveState()
}
